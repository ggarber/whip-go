package main

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/pion/webrtc/v2"
)

func main() {
	if len(os.Args) != 3 {
		log.Fatal("Invalid number of arguments, pass endpoint url and token")
	}

	mediaEngine := webrtc.MediaEngine{}

	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{},
	}
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine)).NewPeerConnection(config)
	if err != nil {
		log.Fatal("Unexpected error building peer connection", err)
	}

	answer, err := pc.CreateOffer(nil)
	if err != nil {
		log.Fatal("webrtc could not create answer. ", err)
	}

	log.Println(answer.SDP)
	var sdp = []byte(answer.SDP)
	client := &http.Client{}
	req, err := http.NewRequest("POST", os.Args[1], bytes.NewBuffer(sdp))
	if err != nil {
		log.Fatal("Unexpected error building http request", err)
	}

	req.Header.Add("Content-Type", "application/sdp")
	req.Header.Add("Authorization", "Bearer "+os.Args[2])

	resp, err := client.Do(req)
	if err != nil {
		log.Fatal("Failed http request", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)

	log.Println(string(body))
}
