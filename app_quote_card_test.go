package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func quoteCardTestPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	img.Set(1, 1, color.RGBA{R: 200, G: 80, B: 40, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// stubQuoteCardDialog 把保存框换成桩，记录被调用的次数与默认文件名。
func stubQuoteCardDialog(t *testing.T, path string, err error) (*int, *string) {
	t.Helper()
	old := selectQuoteCardPath
	t.Cleanup(func() { selectQuoteCardPath = old })
	calls, name := 0, ""
	selectQuoteCardPath = func(_ context.Context, defaultName string) (string, error) {
		calls++
		name = defaultName
		return path, err
	}
	return &calls, &name
}

func TestSaveQuoteCardWritesValidPNG(t *testing.T) {
	data := quoteCardTestPNG(t)
	target := filepath.Join(t.TempDir(), "卡片.png")
	calls, name := stubQuoteCardDialog(t, target, nil)
	saved, err := (&App{ctx: context.Background()}).SaveQuoteCard(base64.StdEncoding.EncodeToString(data), "长片 1-02-03.png")
	if err != nil || saved != target {
		t.Fatalf("saved=%q err=%v", saved, err)
	}
	if *calls != 1 || *name != "长片 1-02-03.png" {
		t.Fatalf("dialog calls=%d name=%q", *calls, *name)
	}
	written, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(written, data) {
		t.Fatalf("written content differs: %v", err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(target), ".quote-card-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary files left behind: %v", leftovers)
	}
}

// 保存框里确认过替换的同名文件直接被新卡片覆盖。
func TestSaveQuoteCardReplacesConfirmedExistingFile(t *testing.T) {
	data := quoteCardTestPNG(t)
	target := filepath.Join(t.TempDir(), "card.png")
	if err := os.WriteFile(target, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	stubQuoteCardDialog(t, target, nil)
	if _, err := (&App{ctx: context.Background()}).SaveQuoteCard(base64.StdEncoding.EncodeToString(data), "card"); err != nil {
		t.Fatal(err)
	}
	written, _ := os.ReadFile(target)
	if !bytes.Equal(written, data) {
		t.Fatal("existing file was not replaced")
	}
}

func TestSaveQuoteCardCancelReturnsEmptyPath(t *testing.T) {
	calls, _ := stubQuoteCardDialog(t, "", nil)
	saved, err := (&App{ctx: context.Background()}).SaveQuoteCard(base64.StdEncoding.EncodeToString(quoteCardTestPNG(t)), "card")
	if err != nil || saved != "" || *calls != 1 {
		t.Fatalf("cancel: saved=%q err=%v calls=%d", saved, err, *calls)
	}
}

func TestSaveQuoteCardRejectsInvalidDataBeforeDialog(t *testing.T) {
	valid := quoteCardTestPNG(t)
	jpegLike := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, valid[4:]...)
	cases := map[string]string{
		"empty":           "",
		"not base64":      "这不是 base64!!",
		"not png":         base64.StdEncoding.EncodeToString([]byte("hello, quote card")),
		"wrong signature": base64.StdEncoding.EncodeToString(jpegLike),
		"truncated":       base64.StdEncoding.EncodeToString(valid[:12]),
		"data url":        "data:image/png;base64," + base64.StdEncoding.EncodeToString(valid),
		"oversized":       strings.Repeat("A", (quoteCardMaxBytes/3+2)*4),
	}
	for label, encoded := range cases {
		t.Run(label, func(t *testing.T) {
			target := filepath.Join(t.TempDir(), "card.png")
			calls, _ := stubQuoteCardDialog(t, target, nil)
			saved, err := (&App{ctx: context.Background()}).SaveQuoteCard(encoded, "card")
			if !errors.Is(err, errQuoteCardInvalid) || saved != "" {
				t.Fatalf("saved=%q err=%v", saved, err)
			}
			if *calls != 0 {
				t.Fatal("dialog opened for invalid data")
			}
			if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
				t.Fatal("invalid data was written")
			}
		})
	}
}

func TestSaveQuoteCardAcceptsDataAtSizeLimit(t *testing.T) {
	// 一张合法 PNG 后面补足到恰好 30 MB：PNG 解码头只读签名与 IHDR，上限按解码后的字节数算。
	data := quoteCardTestPNG(t)
	data = append(data, make([]byte, quoteCardMaxBytes-len(data))...)
	if _, err := decodeQuoteCardPNG(base64.StdEncoding.EncodeToString(data)); err != nil {
		t.Fatalf("data at the limit rejected: %v", err)
	}
	data = append(data, 0)
	if _, err := decodeQuoteCardPNG(base64.StdEncoding.EncodeToString(data)); !errors.Is(err, errQuoteCardInvalid) {
		t.Fatalf("data over the limit accepted: %v", err)
	}
}

func TestSaveQuoteCardDialogAndWriteFailures(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(quoteCardTestPNG(t))
	stubQuoteCardDialog(t, "", errors.New("boom"))
	if _, err := (&App{ctx: context.Background()}).SaveQuoteCard(encoded, "card"); err == nil || err.Error() != "打开保存对话框失败" {
		t.Fatalf("dialog failure: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing", "card.png")
	stubQuoteCardDialog(t, missing, nil)
	saved, err := (&App{ctx: context.Background()}).SaveQuoteCard(encoded, "card")
	if err == nil || saved != "" || strings.Contains(err.Error(), missing) {
		t.Fatalf("write failure missing or leaks path: saved=%q err=%v", saved, err)
	}
	if _, err := (&App{}).SaveQuoteCard(encoded, "card"); err == nil {
		t.Fatal("saved without a window context")
	}
}

func TestQuoteCardDefaultName(t *testing.T) {
	cases := map[string]string{
		"长片 1-02-03.png":         "长片 1-02-03.png",
		"长片 1-02-03":             "长片 1-02-03.png",
		"a/b\\c:d*e?f\"g<h>i|j":  "a_b_c_d_e_f_g_h_i_j.png",
		"..\\..\\etc\\passwd":    "_.._etc_passwd.png",
		"  ":                     "台词卡.png",
		".png":                   "台词卡.png",
		"line\nbreak.PNG":        "line_break.png",
		strings.Repeat("长", 100): strings.Repeat("长", quoteCardNameMaxRunes) + ".png",
	}
	for input, want := range cases {
		if got := quoteCardDefaultName(input); got != want {
			t.Errorf("quoteCardDefaultName(%q) = %q, want %q", input, got, want)
		}
	}
}
