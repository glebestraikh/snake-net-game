package connection

import (
	"log"
	"net"
)

const (
	multicastAddress = "239.192.0.4:9192"
)

func Connection() *net.UDPConn {
	// резолвим multicast-адрес
	multicastUDPAddr, err := net.ResolveUDPAddr("udp4", multicastAddress)
	if err != nil {
		log.Fatalf("Error resolving multicast address: %v", err)
	}

	// создаем сокет для multicast
	multicastConn, err := net.ListenMulticastUDP("udp4", nil, multicastUDPAddr)
	if err != nil {
		log.Fatalf("Error creating multicast socket: %v", err)
	}

	return multicastConn
}
