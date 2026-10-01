package notifychannel

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"
)

// sendSMTP SMTP 邮件发送（对齐 cloud-auto-save-x email 渠道）
func sendSMTP(_ context.Context, cfg map[string]string, msg Message) error {
	host := cfg["host"]
	encryption := orDefault(cfg["encryption"], "starttls")
	port := cfg["port"]
	if port == "" {
		switch encryption {
		case "ssl":
			port = "465"
		case "starttls":
			port = "587"
		default:
			port = "25"
		}
	}

	username := cfg["username"]
	password := cfg["password"]
	from := orDefault(cfg["from"], username)
	fromName := orDefault(cfg["from_name"], "diy-strm")
	toList := strings.Split(cfg["to"], ",")
	for i := range toList {
		toList[i] = strings.TrimSpace(toList[i])
	}
	prefix := orDefault(cfg["subject_prefix"], "")
	subject := prefix + msg.Title
	body := strings.ReplaceAll("From: "+fromName+" <"+from+">\r\nTo: "+strings.Join(toList, ",")+
		"\r\nSubject: "+subject+"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n"+msg.Content, "\n", "\r\n")

	addr := host + ":" + port
	var auth smtp.Auth
	if username != "" && password != "" {
		auth = smtp.PlainAuth("", username, password, host)
	}
	if encryption == "ssl" {
		return sendSMTPSSL(addr, from, toList, []byte(body), auth)
	}
	return smtp.SendMail(addr, auth, from, toList, []byte(body))
}

func sendSMTPSSL(addr, from string, to []string, msg []byte, auth smtp.Auth) error {
	return fmt.Errorf("SSL SMTP 暂未实现，请用 starttls")
}
