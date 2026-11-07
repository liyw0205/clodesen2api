package alerts

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

type smtpUnencryptedAuth struct {
	user, password, host string
}

func (auth smtpUnencryptedAuth) Start(server *smtp.ServerInfo) (string, []byte, error) {
	if server.Name != auth.host {
		return "", nil, ErrDelivery
	}
	return "PLAIN", []byte("\x00" + auth.user + "\x00" + auth.password), nil
}

func (smtpUnencryptedAuth) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, ErrDelivery
	}
	return nil, nil
}

func sendSMTP(ctx context.Context, config map[string]string, title, content string) error {
	host, address, err := smtpAddress(config["server"], config["security"])
	if err != nil {
		return err
	}
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return ErrDelivery
	}
	defer conn.Close()
	closeConnection := conn.Close
	stop := context.AfterFunc(ctx, func() { _ = closeConnection() })
	defer stop()
	deadline := time.Now().Add(5 * time.Second)
	if at, ok := ctx.Deadline(); ok && at.Before(deadline) {
		deadline = at
	}
	if conn.SetDeadline(deadline) != nil {
		return ErrDelivery
	}
	tlsConfig := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
	if defaultValue(config["security"], "tls") == "tls" {
		secure := tls.Client(conn, tlsConfig)
		if secure.HandshakeContext(ctx) != nil {
			return ErrDelivery
		}
		conn = secure
	}
	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return ErrDelivery
	}
	defer client.Close()
	if config["security"] == "starttls" {
		if client.StartTLS(tlsConfig) != nil {
			return ErrDelivery
		}
	}
	from, err := mail.ParseAddress(config["email"])
	if err != nil {
		return invalid("邮箱地址无效")
	}
	recipients, err := mail.ParseAddressList(defaultValue(config["to"], config["email"]))
	if err != nil {
		return invalid("收件人地址无效")
	}
	auth := smtp.PlainAuth("", from.Address, config["password"], host)
	if config["security"] == "none" {
		// Plaintext authentication is allowed only for this explicitly selected mode.
		auth = smtpUnencryptedAuth{from.Address, config["password"], host}
	}
	if client.Auth(auth) != nil {
		return ErrDelivery
	}
	if client.Mail(from.Address) != nil {
		return ErrDelivery
	}
	to := []string{}
	for _, recipient := range recipients {
		if client.Rcpt(recipient.Address) != nil {
			return ErrDelivery
		}
		to = append(to, recipient.String())
	}
	from.Name = defaultValue(config["name"], from.Name)
	message := "From: " + from.String() + "\r\nTo: " + strings.Join(to, ", ") +
		"\r\nSubject: " + mime.BEncoding.Encode("UTF-8", title) +
		"\r\nDate: " + time.Now().Format(time.RFC1123Z) +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n"
	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	for len(encoded) > 76 {
		message += encoded[:76] + "\r\n"
		encoded = encoded[76:]
	}
	message += encoded + "\r\n"
	writer, err := client.Data()
	if err != nil {
		return ErrDelivery
	}
	if _, err := writer.Write([]byte(message)); err != nil {
		return ErrDelivery
	}
	if writer.Close() != nil {
		return ErrDelivery
	}
	// DATA acceptance is the delivery boundary; QUIT failure must not duplicate mail.
	_ = client.Quit()
	return nil
}
