package alerts

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strings"
	"testing"
	"time"
)

func TestSMTPExplicitUnencryptedAuthentication(t *testing.T) {
	auth := smtpUnencryptedAuth{"sender@example.test", "test-password", "smtp.example.test"}
	method, payload, err := auth.Start(&smtp.ServerInfo{Name: "smtp.example.test"})
	if err != nil || method != "PLAIN" || string(payload) != "\x00sender@example.test\x00test-password" {
		t.Fatalf("explicit plaintext authentication failed: method=%s, error=%v", method, err)
	}
	if _, _, err := auth.Start(&smtp.ServerInfo{Name: "other.example.test"}); !errors.Is(err, ErrDelivery) {
		t.Fatal("unexpected SMTP server accepted")
	}
	if _, err := auth.Next(nil, true); !errors.Is(err, ErrDelivery) {
		t.Fatal("unexpected SMTP challenge accepted")
	}
	if _, err := auth.Next(nil, false); err != nil {
		t.Fatal(err)
	}
}

func TestSMTPDeliveryAndRecipientRejection(t *testing.T) {
	for _, reject := range []bool{false, true} {
		name := "accepted"
		if reject {
			name = "rejected"
		}
		t.Run(name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			type received struct {
				message []byte
				auth    string
				err     error
			}
			result := make(chan received, 1)
			go func() {
				var value received
				defer func() { result <- value }()
				conn, err := listener.Accept()
				if err != nil {
					value.err = err
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				protocol := textproto.NewConn(conn)
				_ = protocol.PrintfLine("220 localhost SMTP")
				for {
					line, err := protocol.ReadLine()
					if err != nil {
						value.err = err
						return
					}
					switch {
					case strings.HasPrefix(line, "EHLO "):
						_ = protocol.PrintfLine("250-localhost\r\n250 AUTH PLAIN")
					case strings.HasPrefix(line, "AUTH PLAIN "):
						decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
						if err != nil {
							value.err = err
							return
						}
						value.auth = string(decoded)
						_ = protocol.PrintfLine("235 authenticated")
					case strings.HasPrefix(line, "MAIL FROM:"), strings.HasPrefix(line, "RCPT TO:"):
						if reject && strings.HasPrefix(line, "RCPT TO:") {
							_ = protocol.PrintfLine("550 recipient rejected")
							return
						}
						_ = protocol.PrintfLine("250 accepted")
					case line == "DATA":
						_ = protocol.PrintfLine("354 send message")
						value.message, value.err = protocol.ReadDotBytes()
						if value.err != nil {
							return
						}
						_ = protocol.PrintfLine("250 queued")
					case line == "QUIT":
						_ = protocol.PrintfLine("221 bye")
						return
					default:
						value.err = errors.New("unexpected SMTP command: " + line)
						return
					}
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			err = sendSMTP(ctx, map[string]string{
				"server": listener.Addr().String(), "security": "none",
				"email": "sender@example.test", "password": "test-password", "name": "通知服务",
				"to": "ops@example.test, other@example.test",
			}, "测试告警", "第一行\n第二行")
			if reject && !errors.Is(err, ErrDelivery) || !reject && err != nil {
				t.Fatalf("SMTP result = %v", err)
			}
			var value received
			select {
			case value = <-result:
			case <-ctx.Done():
				t.Fatal("SMTP receiver did not finish")
			}
			if value.err != nil {
				t.Fatal(value.err)
			}
			if value.auth != "\x00sender@example.test\x00test-password" {
				t.Fatal("SMTP credentials were not sent correctly")
			}
			if reject {
				if len(value.message) > 0 {
					t.Fatal("rejected recipient still received a message")
				}
				return
			}
			message, err := mail.ReadMessage(strings.NewReader(string(value.message)))
			if err != nil {
				t.Fatal(err)
			}
			subject, err := (&mime.WordDecoder{}).DecodeHeader(message.Header.Get("Subject"))
			if err != nil || subject != "测试告警" || !strings.Contains(message.Header.Get("To"), "other@example.test") {
				t.Fatal("SMTP headers were not encoded correctly")
			}
			body, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, message.Body))
			if err != nil || string(body) != "第一行\n第二行" {
				t.Fatalf("SMTP body = %q, %v", body, err)
			}
		})
	}
}
