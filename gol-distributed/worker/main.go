package main

import (
	"flag"
	"fmt"
	"net"
	"net/rpc"
)

func main() {
	port := flag.String("port", "8040", "Port to listen on")
	flag.Parse()

	worker := new(GameWorker)

	rpc.Register(worker)

	l, err := net.Listen("tcp", ":"+*port)
	if err != nil {
		fmt.Println("Error listening:", err)
		return
	}
	defer l.Close()

	fmt.Println("Worker server listening on port", *port)
	fmt.Println()

	for {
		conn, err := l.Accept()
		if err != nil {
			fmt.Printf("Worker Accept error: %v\n", err)
			continue
		}
		go rpc.ServeConn(conn)
	}
}
