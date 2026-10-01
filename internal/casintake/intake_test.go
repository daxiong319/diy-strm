package casintake

import (
	"errors"
	"testing"
)

func TestIsPollingConflict(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"普通网络错误", errors.New("dial tcp: i/o timeout"), false},
		{"Telegram 官方冲突文案", errors.New(`Telegram 返回错误: Conflict: terminated by other getUpdates request; make sure that only one bot instance is running`), true},
		{"仅 Conflict 关键字", errors.New("Conflict"), true},
		{"terminated 文案（大写）", errors.New("TERMINATED BY OTHER GETUPDATES REQUEST"), true},
		{"其它 Telegram 错误", errors.New("Telegram 返回错误: Bad Request: chat not found"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isPollingConflict(tc.err); got != tc.want {
				t.Fatalf("isPollingConflict(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsCasFileName(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"movie.cas", true},
		{"MOVIE.CAS", true},
		{"  spaced.cas  ", true},
		{"movie.mkv", false},
		{"movie", false},
		{".cas", true},
		{"", false},
	}
	for _, tc := range cases {
		if got := isCasFileName(tc.in); got != tc.want {
			t.Fatalf("isCasFileName(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
