// streamclient 是 P5 轻量实时推流的验证客户端：
// 以 20 ms 节拍向 ws://.../v1/stream 推送合成语音帧（可注入丢包），结束时读取服务端统计。
// 丢包注入只跳过发送、不补发，与抖动缓冲单测配合证明“丢包可恢复、流不中断”。
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"os"
	"time"

	"golang.org/x/net/websocket"
)

func main() {
	url := flag.String("url", "ws://127.0.0.1:8082/v1/stream", "推流地址")
	token := flag.String("token", os.Getenv("API_BEARER_TOKEN"), "Bearer token")
	seconds := flag.Int("seconds", 5, "推流时长（秒）")
	dropPercent := flag.Int("drop", 0, "人工丢包百分比 0-90")
	flag.Parse()

	config, err := websocket.NewConfig(*url, "http://127.0.0.1/")
	if err != nil {
		exit(err)
	}
	config.Header.Set("Authorization", "Bearer "+*token)
	conn, err := websocket.DialConfig(config)
	if err != nil {
		exit(err)
	}
	defer conn.Close()

	const framesPerSecond = 50 // 20 ms/帧
	totalFrames := *seconds * framesPerSecond
	state := uint32(42)
	nextRandom := func() int {
		state = state*1664525 + 1013904223
		return int((state >> 16) % 100)
	}
	sent, dropped := 0, 0
	started := time.Now()
	for frame := 0; frame < totalFrames; frame++ {
		deadline := started.Add(time.Duration(frame*20) * time.Millisecond)
		if sleep := time.Until(deadline); sleep > 0 {
			time.Sleep(sleep)
		}
		if frame > 0 && nextRandom() < *dropPercent {
			dropped++
			continue
		}
		payload := make([]byte, 4+640)
		binary.BigEndian.PutUint32(payload[:4], uint32(frame))
		for i := 0; i < 320; i++ {
			value := int16(math.Sin(2*math.Pi*440*float64(frame*320+i)/16000) * 12000)
			binary.LittleEndian.PutUint16(payload[4+i*2:], uint16(value))
		}
		if err := websocket.Message.Send(conn, payload); err != nil {
			exit(err)
		}
		sent++
	}
	if err := websocket.Message.Send(conn, "END"); err != nil {
		exit(err)
	}
	var stats string
	if err := websocket.Message.Receive(conn, &stats); err != nil {
		exit(err)
	}
	fmt.Printf("推流完成：sent=%d injected_drops=%d 服务端统计=%s\n", sent, dropped, stats)
}

func exit(err error) {
	fmt.Fprintln(os.Stderr, "streamclient 失败：", err)
	os.Exit(1)
}
