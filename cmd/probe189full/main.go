package main

import (
	"context"
	"fmt"
	"time"

	"diy-strm/internal/cloud189"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	sess := cloud189.NewQrSession()
	qr, err := sess.QrInit(ctx)
	if err != nil {
		fmt.Println("QrInit 失败:", err)
		return
	}
	fmt.Println("QrInit OK, 二维码内容:", qr[:80], "...")
	// 轮询一次看 waiting 结构
	status, session, err := sess.QrPollStatus(ctx)
	fmt.Printf("Poll#1: status=%s err=%v session=%v\n", status, err, session)
}
