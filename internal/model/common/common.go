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

// MessageEntry структура для отслеживания неподтвержденных сообщений
type MessageEntry struct {
	msg       *pb.GameMessage
	addr      *net.UDPAddr
	timestamp time.Time
}

// Node общая структура для хранения информации об игроке или мастере
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

	// время последнего сообщения от игрока [playerId]time
	LastInteraction map[int32]time.Time
	// время отправки последнего сообщения игроку отправок сообщений
	LastSent map[string]time.Time

	unconfirmedMessages map[int64]*MessageEntry
	Mu                  sync.Mutex
	Cond                *sync.Cond
	AckChan             chan int64
	stopChan            chan struct{}  // канал для остановки горутин Node
	wg                  sync.WaitGroup // для отслеживания завершения горутин
	stopped             atomic.Bool    // атомарный флаг остановки

	// KnownAddrs хранит последнее известное UDP-адреса для игрока по playerId
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
		AckChan:             make(chan int64, 100), // Буферизованный канал для предотвращения блокировок
		stopChan:            make(chan struct{}),
		KnownAddrs:          make(map[int32]*net.UDPAddr),
	}

	node.Cond = sync.NewCond(&node.Mu)

	return node
}

// StopNodeGoroutines останавливает горутины Node (для перехода из Player в Master)
func (n *Node) StopNodeGoroutines() {
	n.stopped.Store(true) // Устанавливаем атомарный флаг ПЕРЕД закрытием канала
	close(n.stopChan)
}

// WaitForGoroutines ожидает завершения всех горутин Node
func (n *Node) WaitForGoroutines() {
	n.wg.Wait()
	log.Printf("All Node goroutines finished")
}

// ResetStopChan создает новый канал остановки (для использования после смены роли)
func (n *Node) ResetStopChan() {
	n.stopped.Store(false) // Сбрасываем атомарный флаг
	n.stopChan = make(chan struct{})
}

// GetLocalIP получения реального ip
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

			// IPv4
			if ip == nil || ip.IsLoopback() || ip.To4() == nil {
				continue
			}

			return ip.String(), nil
		}
	}

	return "", fmt.Errorf("no connected network interface found")
}

// SendAck любое сообщение подтверждается отправкой в ответ сообщения AckMsg с таким же msg_seq
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

// GetPlayerIdByAddress id игрока по адресу
func (n *Node) GetPlayerIdByAddress(addr *net.UDPAddr) int32 {
	if n.State == nil {
		return 1
	}

	// Exact match first
	for _, player := range n.State.Players.GetPlayers() {
		if player.GetIpAddress() == addr.IP.String() && int(player.GetPort()) == addr.Port {
			return player.GetId()
		}
	}

	// Fallback for Master: match by IP and role
	// Kotlin Master might omit its own IP/Port or use a different port than the one in announcement.
	// If we have a Master in the state, and the incoming IP matches our known MasterAddr OR the source IP,
	// we assume it's the Master.
	for _, player := range n.State.Players.GetPlayers() {
		if player.GetRole() == pb.NodeRole_MASTER {
			// If IP is provided in list, check it. Otherwise trust the known MasterAddr or just role.
			if (player.GetIpAddress() != "" && player.GetIpAddress() == addr.IP.String()) ||
				(n.MasterAddr != nil && n.MasterAddr.IP.String() == addr.IP.String()) {
				return player.GetId()
			}
		}
	}

	// Final fallback: if we have ONLY ONE master and we received a message from an unknown address,
	// and we are a player, it's highly likely to be the master.
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

// SendMessage маршализация и отправка сообщения
func (n *Node) SendMessage(msg *pb.GameMessage, addr *net.UDPAddr) {
	if n.isStopped() || addr == nil {
		return
	}

	n.Mu.Lock()
	// Для ACK сообщений НЕ инкрементируем MsgSeq и НЕ перезаписываем его,
	// так как он должен совпадать с подтверждаемым сообщением.
	isAck := false
	if _, ok := msg.Type.(*pb.GameMessage_Ack); ok {
		isAck = true
	}

	if !isAck {
		// Инкрементируем только если MsgSeq еще не установлен (например, для новых сообщений)
		if msg.GetMsgSeq() == 0 {
			msg.MsgSeq = proto.Int64(n.MsgSeq)
			n.MsgSeq++
		}
	}

	// Автоматически устанавливаем sender_id если он известен
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

	// Добавляем сообщение в очередь неподтвержденных, если это не ACK и не Announcement/Discover
	if !isAck {
		switch msg.Type.(type) {
		case *pb.GameMessage_Announcement, *pb.GameMessage_Discover:
			// Не добавляем
		default:
			n.unconfirmedMessages[msg.GetMsgSeq()] = &MessageEntry{
				msg:       msg,
				addr:      addr,
				timestamp: time.Now(),
			}
		}
	}
	n.Mu.Unlock()

	// Если адрес мультикастовый — отправляем через UnicastConn (спецификация требует отдельный сокет для всего остального).
	// Это также гарантирует, что исходный адрес/порт совпадает с UnicastConn.LocalAddr(), что ожидают другие клиенты.
	_, err = n.UnicastConn.WriteToUDP(data, addr)
	if err != nil {
		log.Printf("Error sending Message to %v: %v", addr, err)
		return
	}

	n.Mu.Lock()
	n.LastSent[addr.String()] = time.Now()
	n.Mu.Unlock()
}

// HandleAck обработка полученных AckMsg
func (n *Node) HandleAck(seq int64) {
	if _, exists := n.unconfirmedMessages[seq]; exists {
		delete(n.unconfirmedMessages, seq)
	}
}

// ClearUnconfirmedMessages очищает очередь неподтвержденных сообщений
func (n *Node) ClearUnconfirmedMessages() {
	n.unconfirmedMessages = make(map[int64]*MessageEntry)
	log.Printf("Cleared all unconfirmed messages")
}

// UnconfirmedMessages возвращает количество неподтвержденных сообщений
func (n *Node) UnconfirmedMessages() int {
	return len(n.unconfirmedMessages)
}

// RemoveUnconfirmedMessagesForAddr удаляет все неподтвержденные сообщения для конкретного адреса
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

// RedirectUnconfirmedMessages переадресует неподтвержденные сообщения с одного адреса на другой
func (n *Node) RedirectUnconfirmedMessages(oldAddr, newAddr *net.UDPAddr) {
	for _, entry := range n.unconfirmedMessages {
		if entry.addr.IP.Equal(oldAddr.IP) && entry.addr.Port == oldAddr.Port {
			entry.addr = newAddr
			log.Printf("Redirected unconfirmed message to new MASTER")
		}
	}
}

// isStopped проверяет, остановлен ли Node
// CompressSnake converts a snake represented as a list of absolute coordinates
// (head first, then subsequent body cells) into the protobuf "key points" format:
// first point is absolute head coordinate, each next point is a displacement (either x or y)
// relative to the previous key point. This matches protocol expectations and Kotlin client.
func CompressSnake(s *pb.GameState_Snake, width, height int32) *pb.GameState_Snake {
	if s == nil || len(s.GetPoints()) == 0 {
		return s
	}
	pts := s.GetPoints()
	// copy head as absolute
	head := pts[0]
	newPoints := []*pb.GameState_Coord{{X: proto.Int32(head.GetX()), Y: proto.Int32(head.GetY())}}
	if len(pts) == 1 {
		// only head
		return &pb.GameState_Snake{
			PlayerId:      proto.Int32(s.GetPlayerId()),
			Points:        newPoints,
			State:         s.State,
			HeadDirection: s.HeadDirection,
		}
	}

	// helper to compute wrapped delta between prev and cur
	wrapDelta := func(prev, cur *pb.GameState_Coord) (int32, int32) {
		dx := cur.GetX() - prev.GetX()
		dy := cur.GetY() - prev.GetY()
		// normalize with torus (choose minimal displacement)
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

	// accumulate runs of same axis
	prev := pts[0]
	var accum int32 = 0
	var axisIsX *bool = nil // nil = not initialized; true=x, false=y

	for i := 1; i < len(pts); i++ {
		cur := pts[i]
		dx, dy := wrapDelta(prev, cur)
		// determine this step's axis and value
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
			// start new run
			b := stepIsX
			axisIsX = &b
			accum = val
		} else if *axisIsX == stepIsX {
			// same axis, accumulate
			accum += val
		} else {
			// axis changed — flush previous run
			if *axisIsX {
				newPoints = append(newPoints, &pb.GameState_Coord{X: proto.Int32(accum), Y: proto.Int32(0)})
			} else {
				newPoints = append(newPoints, &pb.GameState_Coord{X: proto.Int32(0), Y: proto.Int32(accum)})
			}
			// start new run
			b := stepIsX
			axisIsX = &b
			accum = val
		}
		prev = cur
	}
	// flush final run
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

// CompressGameState returns a deep copy of the game state with all snakes compressed
// into turning points representation. Original state is not mutated.
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

// StartResendUnconfirmedMessages запускает горутину переотправки с правильной регистрацией в WaitGroup
func (n *Node) StartResendUnconfirmedMessages(stateDelayMs int32) {
	n.wg.Add(1)
	go n.resendUnconfirmedMessagesLoop(stateDelayMs)
}

// resendUnconfirmedMessagesLoop проверка и переотправка неподтвержденных сообщений
func (n *Node) resendUnconfirmedMessagesLoop(stateDelayMs int32) {
	defer n.wg.Done()
	defer log.Printf("Node ResendUnconfirmedMessages goroutine exited")

	ticker := time.NewTicker(time.Duration(stateDelayMs/10) * time.Millisecond)
	defer ticker.Stop()

	for {
		// Проверяем остановку в начале каждой итерации
		if n.isStopped() {
			log.Printf("Node ResendUnconfirmedMessages stopped (isStopped check)")
			return
		}

		select {
		case <-n.stopChan:
			log.Printf("Node ResendUnconfirmedMessages stopped")
			return
		case <-ticker.C:
			// Проверяем остановку после тикера
			if n.isStopped() {
				log.Printf("Node ResendUnconfirmedMessages stopped (after ticker)")
				return
			}

			n.Mu.Lock()
			// Проверяем, есть ли вообще сообщения для переотправки
			if len(n.unconfirmedMessages) == 0 {
				n.Mu.Unlock()
				continue
			}

			now := time.Now()
			// Копируем map для итерации
			messagesToResend := make(map[int64]*MessageEntry)
			for seq, entry := range n.unconfirmedMessages {
				if now.Sub(entry.timestamp) > time.Duration(n.Config.GetStateDelayMs()/10)*time.Millisecond {
					messagesToResend[seq] = entry
				}
			}
			n.Mu.Unlock()

			// Переотправляем сообщения
			for seq, entry := range messagesToResend {
				// Проверяем остановку перед каждой отправкой
				if n.isStopped() {
					log.Printf("Node ResendUnconfirmedMessages stopped (during resend)")
					return
				}

				data, err := proto.Marshal(entry.msg)
				if err != nil {
					log.Printf("Error marshalling Message: %v", err)
					continue
				}
				// Если адрес мультикастовый — при переотправке также используем UnicastConn
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

				// Обновляем timestamp
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

// StartSendPings запускает горутину отправки ping с правильной регистрацией в WaitGroup
func (n *Node) StartSendPings(stateDelayMs int32) {
	n.wg.Add(1)
	go n.sendPingsLoop(stateDelayMs)
}

// sendPingsLoop отправка PingMsg, если не было отправлено сообщений в течение stateDelayMs/10
func (n *Node) sendPingsLoop(stateDelayMs int32) {
	defer n.wg.Done()
	defer log.Printf("Node SendPings goroutine exited")

	ticker := time.NewTicker(time.Duration(stateDelayMs/10) * time.Millisecond)
	defer ticker.Stop()

	for {
		// Проверяем остановку в начале каждой итерации
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

		// Проверяем остановку после тикера
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
			// Мастер пингует всех игроков, кроме себя
			for _, player := range n.State.Players.Players {
				// Проверяем остановку перед отправкой каждого ping
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
			// Обычный игрок пингует только мастера, если мастер известен
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
