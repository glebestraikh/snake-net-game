package common

import (
	"fmt"
	"google.golang.org/protobuf/proto"
	"log"
	"net"
	pb "snake-net-game/pkg/proto"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const MulticastAddr = "239.192.0.4:9192"

type MessageEntry struct {
	msg       *pb.GameMessage
	addr      *net.UDPAddr
	timestamp time.Time
}

type Node struct {
	State            *pb.GameState
	Config           *pb.GameConfig
	MulticastAddress string
	MulticastConn    *net.UDPConn
	UnicastConn      *net.UDPConn
	PlayerInfo       *pb.GamePlayer
	MsgSeq           int64
	Role             pb.NodeRole

	MasterAddr *net.UDPAddr

	LastInteraction map[int32]time.Time

	LastSent map[string]time.Time

	unconfirmedMessages map[int64]*MessageEntry
	Mu                  sync.Mutex
	Cond                *sync.Cond
	AckChan             chan int64
	stopChan            chan struct{}
	wg                  sync.WaitGroup
	stopped             atomic.Bool

	KnownAddrs map[int32]*net.UDPAddr
}

func NewNode(state *pb.GameState, config *pb.GameConfig, multicastConn *net.UDPConn,
	unicastConn *net.UDPConn, playerInfo *pb.GamePlayer) *Node {
	node := &Node{
		State:            state,
		Config:           config,
		MulticastAddress: MulticastAddr,
		MulticastConn:    multicastConn,
		UnicastConn:      unicastConn,
		PlayerInfo:       playerInfo,
		MsgSeq:           1,

		LastInteraction:     make(map[int32]time.Time),
		LastSent:            make(map[string]time.Time),
		unconfirmedMessages: make(map[int64]*MessageEntry),
		AckChan:             make(chan int64, 100),
		stopChan:            make(chan struct{}),
		KnownAddrs:          make(map[int32]*net.UDPAddr),
	}

	node.Cond = sync.NewCond(&node.Mu)

	return node
}

func (n *Node) StopNodeGoroutines() {
	n.stopped.Store(true)
	close(n.stopChan)
}

func (n *Node) WaitForGoroutines() {
	n.wg.Wait()
	log.Printf("All Node goroutines finished")
}

func (n *Node) ResetStopChan() {
	n.stopped.Store(false) // Сбрасываем атомарный флаг
	n.stopChan = make(chan struct{})
}

func GetLocalIP() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("error getting network interfaces: %w", err)
	}

	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}

			if ip == nil || ip.IsLoopback() || ip.To4() == nil {
				continue
			}

			return ip.String(), nil
		}
	}

	return "", fmt.Errorf("no connected network interface found")
}

func (n *Node) SendAck(msg *pb.GameMessage, addr *net.UDPAddr) {
	switch msg.Type.(type) {
	case *pb.GameMessage_Announcement, *pb.GameMessage_Discover, *pb.GameMessage_Ack:
		return
	}

	receiverId := msg.GetSenderId()
	if receiverId == 0 {
		receiverId = n.GetPlayerIdByAddress(addr)
	}

	ackMsg := &pb.GameMessage{
		MsgSeq:     proto.Int64(msg.GetMsgSeq()),
		SenderId:   proto.Int32(n.PlayerInfo.GetId()),
		ReceiverId: proto.Int32(receiverId),
		Type: &pb.GameMessage_Ack{
			Ack: &pb.GameMessage_AckMsg{},
		},
	}

	log.Printf("Node: Sending ACK to %v [msgSeq=%d, receiverId=%d, myId=%d]",
		addr, msg.GetMsgSeq(), receiverId, n.PlayerInfo.GetId())

	n.SendMessage(ackMsg, addr)
}

func (n *Node) GetPlayerIdByAddress(addr *net.UDPAddr) int32 {
	if n.State == nil || addr == nil {
		return 0
	}

	for _, player := range n.State.Players.GetPlayers() {
		if player.GetIpAddress() == addr.IP.String() && int(player.GetPort()) == addr.Port {
			return player.GetId()
		}
	}

	for _, player := range n.State.Players.GetPlayers() {
		if player.GetRole() == pb.NodeRole_MASTER {
			if (player.GetIpAddress() != "" && player.GetIpAddress() == addr.IP.String()) ||
				(n.MasterAddr != nil && n.MasterAddr.IP.String() == addr.IP.String()) {
				return player.GetId()
			}
		}
	}

	if n.Role != pb.NodeRole_MASTER {
		for _, player := range n.State.Players.GetPlayers() {
			if player.GetRole() == pb.NodeRole_MASTER {
				return player.GetId()
			}
		}
	}

	return 0
}

// SendPing отправка
func (n *Node) SendPing(addr *net.UDPAddr) {
	pingMsg := &pb.GameMessage{
		MsgSeq:   proto.Int64(n.MsgSeq),
		SenderId: proto.Int32(n.PlayerInfo.GetId()),
		Type: &pb.GameMessage_Ping{
			Ping: &pb.GameMessage_PingMsg{},
		},
	}

	n.SendMessage(pingMsg, addr)
}

func (n *Node) SendMessage(msg *pb.GameMessage, addr *net.UDPAddr) {
	if n.isStopped() || addr == nil {
		return
	}

	n.Mu.Lock()
	isAck := false
	if _, ok := msg.Type.(*pb.GameMessage_Ack); ok {
		isAck = true
	}

	if !isAck {
		if msg.GetMsgSeq() == 0 {
			msg.MsgSeq = proto.Int64(n.MsgSeq)
			n.MsgSeq++
		}
	}

	if n.PlayerInfo != nil && n.PlayerInfo.GetId() > 0 {
		if msg.GetSenderId() == 0 {
			msg.SenderId = proto.Int32(n.PlayerInfo.GetId())
		}
	}

	data, err := proto.Marshal(msg)
	if err != nil {
		log.Printf("Error marshalling Message: %v", err)
		n.Mu.Unlock()
		return
	}

	if !isAck {
		switch msg.Type.(type) {
		case *pb.GameMessage_Announcement, *pb.GameMessage_Discover:
		default:
			n.unconfirmedMessages[msg.GetMsgSeq()] = &MessageEntry{
				msg:       msg,
				addr:      addr,
				timestamp: time.Now(),
			}
		}
	}
	n.Mu.Unlock()

	_, err = n.UnicastConn.WriteToUDP(data, addr)
	if err != nil {
		log.Printf("Error sending Message to %v: %v", addr, err)
		return
	}

	n.Mu.Lock()
	n.LastSent[addr.String()] = time.Now()
	n.Mu.Unlock()
}

func (n *Node) HandleAck(seq int64) {
	if _, exists := n.unconfirmedMessages[seq]; exists {
		delete(n.unconfirmedMessages, seq)
	}
}

func (n *Node) ClearUnconfirmedMessages() {
	n.unconfirmedMessages = make(map[int64]*MessageEntry)
	log.Printf("Cleared all unconfirmed messages")
}

func (n *Node) UnconfirmedMessages() int {
	return len(n.unconfirmedMessages)
}

func (n *Node) RemoveUnconfirmedMessagesForAddr(addr *net.UDPAddr) {
	removed := 0
	for seq, entry := range n.unconfirmedMessages {
		if entry.addr.IP.Equal(addr.IP) && entry.addr.Port == addr.Port {
			delete(n.unconfirmedMessages, seq)
			removed++
		}
	}
	if removed > 0 {
		log.Printf("Removed %d unconfirmed messages for address %v", removed, addr)
	}
}

func (n *Node) RedirectUnconfirmedMessages(oldAddr, newAddr *net.UDPAddr) {
	for _, entry := range n.unconfirmedMessages {
		if entry.addr.IP.Equal(oldAddr.IP) && entry.addr.Port == oldAddr.Port {
			entry.addr = newAddr
			log.Printf("Redirected unconfirmed message to new MASTER")
		}
	}
}

func CompressSnake(s *pb.GameState_Snake, width, height int32) *pb.GameState_Snake {
	if s == nil || len(s.GetPoints()) == 0 {
		return s
	}
	pts := s.GetPoints()
	head := pts[0]
	newPoints := []*pb.GameState_Coord{{X: proto.Int32(head.GetX()), Y: proto.Int32(head.GetY())}}
	if len(pts) == 1 {
		return &pb.GameState_Snake{
			PlayerId:      proto.Int32(s.GetPlayerId()),
			Points:        newPoints,
			State:         s.State,
			HeadDirection: s.HeadDirection,
		}
	}

	wrapDelta := func(prev, cur *pb.GameState_Coord) (int32, int32) {
		dx := cur.GetX() - prev.GetX()
		dy := cur.GetY() - prev.GetY()
		if dx > width/2 {
			dx -= width
		} else if dx < -width/2 {
			dx += width
		}
		if dy > height/2 {
			dy -= height
		} else if dy < -height/2 {
			dy += height
		}
		return dx, dy
	}

	prev := pts[0]
	var accum int32 = 0
	var axisIsX *bool = nil

	for i := 1; i < len(pts); i++ {
		cur := pts[i]
		dx, dy := wrapDelta(prev, cur)
		var stepIsX bool
		var val int32
		if dx != 0 {
			stepIsX = true
			val = dx
		} else {
			stepIsX = false
			val = dy
		}

		if axisIsX == nil {
			b := stepIsX
			axisIsX = &b
			accum = val
		} else if *axisIsX == stepIsX {
			accum += val
		} else {
			if *axisIsX {
				newPoints = append(newPoints, &pb.GameState_Coord{X: proto.Int32(accum), Y: proto.Int32(0)})
			} else {
				newPoints = append(newPoints, &pb.GameState_Coord{X: proto.Int32(0), Y: proto.Int32(accum)})
			}
			b := stepIsX
			axisIsX = &b
			accum = val
		}
		prev = cur
	}
	if axisIsX != nil {
		if *axisIsX {
			newPoints = append(newPoints, &pb.GameState_Coord{X: proto.Int32(accum), Y: proto.Int32(0)})
		} else {
			newPoints = append(newPoints, &pb.GameState_Coord{X: proto.Int32(0), Y: proto.Int32(accum)})
		}
	}

	return &pb.GameState_Snake{
		PlayerId:      proto.Int32(s.GetPlayerId()),
		Points:        newPoints,
		State:         s.State,
		HeadDirection: s.HeadDirection,
	}
}

func CompressGameState(state *pb.GameState, config *pb.GameConfig) *pb.GameState {
	if state == nil {
		return nil
	}
	newState := proto.Clone(state).(*pb.GameState)
	for i, snake := range newState.Snakes {
		newState.Snakes[i] = CompressSnake(snake, config.GetWidth(), config.GetHeight())
	}
	return newState
}

func (n *Node) isStopped() bool {
	return n.stopped.Load()
}

func (n *Node) StartResendUnconfirmedMessages(stateDelayMs int32) {
	n.wg.Add(1)
	go n.resendUnconfirmedMessagesLoop(stateDelayMs)
}

func (n *Node) resendUnconfirmedMessagesLoop(stateDelayMs int32) {
	defer n.wg.Done()
	defer log.Printf("Node ResendUnconfirmedMessages goroutine exited")

	ticker := time.NewTicker(time.Duration(stateDelayMs/10) * time.Millisecond)
	defer ticker.Stop()

	for {
		if n.isStopped() {
			log.Printf("Node ResendUnconfirmedMessages stopped (isStopped check)")
			return
		}

		select {
		case <-n.stopChan:
			log.Printf("Node ResendUnconfirmedMessages stopped")
			return
		case <-ticker.C:
			if n.isStopped() {
				log.Printf("Node ResendUnconfirmedMessages stopped (after ticker)")
				return
			}

			n.Mu.Lock()

			if len(n.unconfirmedMessages) == 0 {
				n.Mu.Unlock()
				continue
			}

			now := time.Now()
			messagesToResend := make(map[int64]*MessageEntry)
			for seq, entry := range n.unconfirmedMessages {
				if now.Sub(entry.timestamp) > time.Duration(n.Config.GetStateDelayMs()/10)*time.Millisecond {
					messagesToResend[seq] = entry
				}
			}
			n.Mu.Unlock()

			for seq, entry := range messagesToResend {
				if n.isStopped() {
					log.Printf("Node ResendUnconfirmedMessages stopped (during resend)")
					return
				}

				data, err := proto.Marshal(entry.msg)
				if err != nil {
					log.Printf("Error marshalling Message: %v", err)
					continue
				}
				if entry.addr != nil && entry.addr.IP != nil && entry.addr.IP.IsMulticast() {
					_, err = n.UnicastConn.WriteToUDP(data, entry.addr)
					if err != nil {
						fmt.Printf("Error resending Message via UnicastConn to multicast addr: %v", err)
						continue
					}
				} else {
					_, err = n.UnicastConn.WriteToUDP(data, entry.addr)
					if err != nil {
						fmt.Printf("Error sending Message: %v", err)
						continue
					}
				}

				n.Mu.Lock()
				if existingEntry, exists := n.unconfirmedMessages[seq]; exists {
					existingEntry.timestamp = time.Now()
				}
				n.Mu.Unlock()

				log.Printf("Resent message with Seq: %d to %v from %v", seq, entry.addr, n.PlayerInfo.GetIpAddress()+":"+strconv.Itoa(int(n.PlayerInfo.GetPort())))
				log.Printf(entry.msg.String())
			}
		case seq, ok := <-n.AckChan:
			if !ok {
				log.Printf("Node ResendUnconfirmedMessages stopped (AckChan closed)")
				return
			}
			n.Mu.Lock()
			n.HandleAck(seq)
			n.Mu.Unlock()
		}
	}
}

func (n *Node) StartSendPings(stateDelayMs int32) {
	n.wg.Add(1)
	go n.sendPingsLoop(stateDelayMs)
}

func (n *Node) sendPingsLoop(stateDelayMs int32) {
	defer n.wg.Done()
	defer log.Printf("Node SendPings goroutine exited")

	ticker := time.NewTicker(time.Duration(stateDelayMs/10) * time.Millisecond)
	defer ticker.Stop()

	for {
		if n.isStopped() {
			log.Printf("Node SendPings stopped (isStopped check)")
			return
		}

		select {
		case <-n.stopChan:
			log.Printf("Node SendPings stopped")
			return
		case <-ticker.C:
		}

		if n.isStopped() {
			log.Printf("Node SendPings stopped (after ticker)")
			return
		}

		now := time.Now()
		n.Mu.Lock()
		if n.State == nil {
			n.Mu.Unlock()
			continue
		}
		if n.Role == pb.NodeRole_MASTER {
			for _, player := range n.State.Players.Players {
				if n.isStopped() {
					n.Mu.Unlock()
					log.Printf("Node SendPings stopped (during master ping loop)")
					return
				}

				if player.GetId() == n.PlayerInfo.GetId() {
					continue
				}
				addrKey := fmt.Sprintf("%s:%d", player.GetIpAddress(), player.GetPort())
				last, exists := n.LastSent[addrKey]
				if !exists || now.Sub(last) > time.Duration(n.Config.GetStateDelayMs()/10)*time.Millisecond {
					playerAddr, err := net.ResolveUDPAddr("udp", addrKey)
					if err != nil {
						log.Printf("Error resolving address for Ping: %v", err)
						continue
					}
					n.Mu.Unlock()
					n.SendPing(playerAddr)
					n.Mu.Lock()
					n.LastSent[addrKey] = now
				}
			}
			n.Mu.Unlock()
		} else {
			if n.MasterAddr != nil {
				addrKey := n.MasterAddr.String()
				last, exists := n.LastSent[addrKey]
				if !exists || now.Sub(last) > time.Duration(n.Config.GetStateDelayMs()/10)*time.Millisecond {
					n.Mu.Unlock()
					n.SendPing(n.MasterAddr)
					n.Mu.Lock()
					n.LastSent[addrKey] = now
				}
			}
			n.Mu.Unlock()
		}
	}
}
