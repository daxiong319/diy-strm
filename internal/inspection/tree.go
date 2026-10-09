package inspection

import (
	"context"
	"sort"
	"strings"
)

// TreeNode 是巡检看到的一个网盘节点（扁平表示）。
//
// 刻意做成扁平而不是树：六个检查器里五个要的是「整棵树的全貌」，
// 而不是某一层的子节点。扁平清单可以直接做集合运算（差集、分组、路径前缀），
// 递归遍历反而要在每个检查器里重写一遍。
type TreeNode struct {
	ID        string
	ParentID  string
	Name      string
	Path      string
	IsDir     bool
	Size      int64
	Sha1      string
	AccountID int64
	RootID    string
}

// Root 是一个巡检范围：一个网盘账号下的一个目录。
type Root struct {
	AccountID int64
	ID        string
	Path      string
	// Label 供 Finding 与预览展示用，通常是「账号名 / 目录名」。
	Label string
}

// TreeSource 提供某个根下的清单。
//
// 它是巡检与 driver 之间唯一的通道，只有一个只读方法。
// 没有 Delete、没有 Move：检查器在类型层面就拿不到改数据的手段，
// 「扫描不会动用户文件」因此不靠自觉，而靠这份接口。
//
// 范围（有哪些根）不在这里：那是配置的事，混进来会让清单实现被迫回答
// 一个它无从知道的问题。各检查器自己带 Roots 函数。
type TreeSource interface {
	List(ctx context.Context, root Root) ([]TreeNode, error)
}

// Tree 是根下清单的内存视图，提供检查器常用的派生数据。
type Tree struct {
	Root   Root
	Nodes  []TreeNode
	byID   map[string]TreeNode
	byPath map[string]TreeNode
}

// NewTree 把清单整理成可查询的视图。
func NewTree(root Root, nodes []TreeNode) *Tree {
	t := &Tree{
		Root:   root,
		Nodes:  nodes,
		byID:   make(map[string]TreeNode, len(nodes)),
		byPath: make(map[string]TreeNode, len(nodes)),
	}
	for _, n := range nodes {
		t.byID[n.ID] = n
		if n.Path != "" {
			t.byPath[normalizePath(n.Path)] = n
		}
	}
	return t
}

func normalizePath(p string) string {
	return strings.Trim(strings.ReplaceAll(strings.TrimSpace(p), "\\", "/"), "/")
}

// Files 只返回文件节点（不含目录）。
func (t *Tree) Files() []TreeNode {
	out := make([]TreeNode, 0, len(t.Nodes))
	for _, n := range t.Nodes {
		if !n.IsDir {
			out = append(out, n)
		}
	}
	return out
}

// Dirs 只返回目录节点。
func (t *Tree) Dirs() []TreeNode {
	out := make([]TreeNode, 0, len(t.Nodes))
	for _, n := range t.Nodes {
		if n.IsDir {
			out = append(out, n)
		}
	}
	return out
}

// Children 返回某目录的直接子节点（目录与文件都算）。
func (t *Tree) Children(parentID string) []TreeNode {
	var out []TreeNode
	for _, n := range t.Nodes {
		if n.ParentID == parentID {
			out = append(out, n)
		}
	}
	return out
}

// Descendants 返回某目录下的全部子孙（不含自身），按路径深度排序。
//
// 深度排序是给「自底向上删空目录」用的：如果先删父目录，
// 它的子目录此时已经不在网盘上了，第二次删除会失败。
func (t *Tree) Descendants(dirID string) []TreeNode {
	var out []TreeNode
	for _, n := range t.Nodes {
		if n.IsDir && n.ID != dirID && t.IsUnder(n.Path, t.byID[dirID].Path) {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool { return pathDepth(out[i].Path) > pathDepth(out[j].Path) })
	return out
}

// IsUnder 判断 child 路径是否在 parent 路径之下（含相等）。
//
// 纯字符串前缀判断在路径上会出现「/Movies/AB」被当成「/Movies/A」的子目录这种误判，
// 所以前缀相等之后必须再要求一个分隔符，或者两者完全相同。
func (t *Tree) IsUnder(child, parent string) bool {
	c := normalizePath(child)
	p := normalizePath(parent)
	if p == "" {
		return true
	}
	if c == p {
		return true
	}
	return strings.HasPrefix(c, p+"/")
}

func pathDepth(p string) int {
	p = normalizePath(p)
	if p == "" {
		return 0
	}
	return strings.Count(p, "/") + 1
}

// MediaExtensions 是巡检判定「这是不是一个媒体文件」用的扩展名集合。
//
// 取各检查器的交集式口径：只有媒体文件才算「目录里有活」，
// 否则一个只有 nfo 的空壳目录会被判成有内容，从而永远清不掉。
var MediaExtensions = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".mov": true, ".wmv": true,
	".m4v": true, ".ts": true, ".m2ts": true, ".rmvb": true, ".flv": true,
	".iso": true, ".mpg": true, ".mpeg": true, ".webm": true,
}

// IsMediaName 判断文件名是否是媒体正文文件。
func IsMediaName(name string) bool {
	idx := strings.LastIndex(name, ".")
	if idx < 0 || idx == len(name)-1 {
		return false
	}
	return MediaExtensions[strings.ToLower(name[idx:])]
}