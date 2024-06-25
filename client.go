package events

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"

	"github.com/urfave/cli/v2"
)

func NewClient(address string) (*Client, error) {
	conn, err := net.Dial("tcp", address)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to server: %v", err)
	}
	return &Client{conn: conn, topics: make(map[string]struct{})}, nil
}

func (c *Client) receiveMessages() {
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := c.conn.Read(buf)
			if err != nil {
				log.Printf("Error reading from server: %v", err)
				return
			}
			if n > 0 {
				var msg Message
				err = json.Unmarshal(buf[:n], &msg)
				if err != nil {
					log.Printf("Error decoding message: %v", err)
					continue
				}
				fmt.Printf("Received: [%s] %s\n", msg.Topic, msg.Message)
			}
		}
	}()
}

func (c *Client) sendCommand(cmd Command) error {
	data, err := json.Marshal(cmd)
	if err != nil {
		return fmt.Errorf("error encoding message: %v", err)
	}
	_, err = c.conn.Write(data)
	_, err = c.conn.Write([]byte("\n"))
	if err != nil {
		return fmt.Errorf("failed to send message: %v", err)
	}
	return nil
}

type Command struct {
	Action  string `json:"action"`
	Topic   string `json:"topic,omitempty"`
	Message string `json:"message,omitempty"`
	Topics  string `json:"topics,omitempty"`
	Name    string `json:"name,omitempty"`
}

type Message struct {
	Topic   string `json:"topic"`
	Message string `json:"message"`
}

func StartClient() {
	app := &cli.App{
		Name:  "pubsub-client",
		Usage: "A client for interacting with the Pub/Sub server",
		Commands: []*cli.Command{
			{
				Name:  "subscribe",
				Usage: "Subscribe to topics",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "name",
						Required: true,
						Usage:    "Client name",
					},
					&cli.StringFlag{
						Name:     "topics",
						Required: true,
						Usage:    "Comma-separated list of topics to subscribe",
					},
				},
				Action: func(cCtx *cli.Context) error {
					client, err := NewClient("localhost:8080")
					if err != nil {
						return fmt.Errorf("failed to create client: %v", err)
					}
					defer client.conn.Close()
					cmd := Command{
						Action: "subscribe",
						Name:   cCtx.String("name"),
						Topics: cCtx.String("topics"),
					}
					err = client.sendCommand(cmd)
					if err != nil {
						return err
					}

					client.receiveMessages()

					// Keep the connection open and wait for incoming messages
					select {}
				},
			},
			{
				Name:  "publish",
				Usage: "Publish a message to a topic",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "topic",
						Required: true,
						Usage:    "Topic to publish to",
					},
					&cli.StringFlag{
						Name:     "data",
						Required: true,
						Usage:    "Message data",
					},
				},
				Action: func(cCtx *cli.Context) error {
					client, err := NewClient("localhost:8080")
					if err != nil {
						return fmt.Errorf("failed to create client: %v", err)
					}
					defer client.conn.Close()
					cmd := Command{
						Action:  "publish",
						Topic:   cCtx.String("topic"),
						Message: cCtx.String("data"),
					}
					return client.sendCommand(cmd)
				},
			},
			{
				Name:  "unsubscribe",
				Usage: "Unsubscribe from topics",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "name",
						Required: true,
						Usage:    "Client name",
					},
					&cli.StringFlag{
						Name:     "topics",
						Required: true,
						Usage:    "Comma-separated list of topics to unsubscribe",
					},
				},
				Action: func(cCtx *cli.Context) error {
					client, err := NewClient("localhost:8080")
					if err != nil {
						return fmt.Errorf("failed to create client: %v", err)
					}
					defer client.conn.Close()
					cmd := Command{
						Action: "unsubscribe",
						Name:   cCtx.String("name"),
						Topics: cCtx.String("topics"),
					}
					return client.sendCommand(cmd)
				},
			},
			{
				Name:  "pause",
				Usage: "Pause receiving messages",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:     "name",
						Required: true,
						Usage:    "Client name",
					},
				},
				Action: func(cCtx *cli.Context) error {
					client, err := NewClient("localhost:8080")
					if err != nil {
						return fmt.Errorf("failed to create client: %v", err)
					}
					defer client.conn.Close()
					cmd := Command{
						Action: "pause",
						Name:   cCtx.String("name"),
					}
					return client.sendCommand(cmd)
				},
			},
		},
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}
