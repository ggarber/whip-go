package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
)

func main() {
	if len(os.Args) != 3 {
		log.Fatal("Invalid number of arguments, pass endpoint url and token")
	}

	whip := NewWHIPClient(os.Args[1], os.Args[2])
	whip.Publish()

	fmt.Print("Press 'Enter' to continue...")
	bufio.NewReader(os.Stdin).ReadBytes('\n')

	whip.Close()
}
