package main

import (
	"context"
	"fmt"
	"os"

	"diy-strm/internal/pan139"
)

func main() {
	authB64, err := os.ReadFile("C:/Users/12630/AppData/Local/Temp/opencode/pan139_auth.txt")
	if err != nil {
		fmt.Println("read auth fail:", err)
		return
	}
	client := pan139.NewClient(3, string(authB64))
	defer client.Close()

	ctx := context.Background()

	page, next, err := client.ListPage(ctx, "", "")
	if err != nil {
		fmt.Println("ListPage(root) ERROR:", err)
		return
	}
	fmt.Printf("ListPage(root) len=%d next=%q\n", len(page), next)
	for i, f := range page {
		if i >= 10 {
			break
		}
		fmt.Printf("  [%d] id=%s dir=%v name=%s\n", i, f.GetID(), f.IsDir(), f.FileName)
	}

	files, err := client.GetFiles(ctx, "root")
	if err != nil {
		fmt.Println("GetFiles(root) ERROR:", err)
		return
	}
	fmt.Printf("GetFiles(root) len=%d\n", len(files))
}
