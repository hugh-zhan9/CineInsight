package services

import (
	"net"
	"net/netip"
	"testing"
)

// 手机端浏览只该给出手机连得上的地址：虚拟网桥（macOS 互联网共享 / 虚拟机）、
// VPN 隧道这些列出来只会让人挨个去试。默认路由那块网卡排最前。
func TestShortFeedLANURLsDropVirtualInterfacesAndRankPrimaryFirst(t *testing.T) {
	interfaces := []lanInterface{
		{Name: "lo0", Flags: net.FlagUp | net.FlagLoopback, Addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1")}},
		{Name: "en0", Flags: net.FlagUp, Addrs: []netip.Addr{netip.MustParseAddr("192.168.162.250")}},
		{Name: "en5", Flags: net.FlagUp, Addrs: []netip.Addr{netip.MustParseAddr("192.168.8.20")}},
		{Name: "bridge100", Flags: net.FlagUp, Addrs: []netip.Addr{netip.MustParseAddr("192.168.139.3")}},
		{Name: "bridge101", Flags: net.FlagUp, Addrs: []netip.Addr{netip.MustParseAddr("192.168.97.0")}},
		{Name: "utun4", Flags: net.FlagUp | net.FlagPointToPoint, Addrs: []netip.Addr{netip.MustParseAddr("10.8.0.2")}},
		{Name: "en9", Flags: 0, Addrs: []netip.Addr{netip.MustParseAddr("192.168.50.7")}},
	}

	urls := buildShortFeedLANURLs(interfaces, netip.MustParseAddr("192.168.162.250"), 18088)

	want := []string{
		"http://192.168.162.250:18088/short/",
		"http://192.168.8.20:18088/short/",
	}
	if len(urls) != len(want) {
		t.Fatalf("只应留下真实可达网卡: %v", urls)
	}
	for i := range want {
		if urls[i] != want[i] {
			t.Fatalf("第 %d 条应为 %s，实际 %s（完整: %v）", i, want[i], urls[i], urls)
		}
	}
}

func TestShortFeedLANURLsKeepCandidatesWhenPrimaryUnknown(t *testing.T) {
	interfaces := []lanInterface{
		{Name: "en0", Flags: net.FlagUp, Addrs: []netip.Addr{netip.MustParseAddr("192.168.162.250")}},
	}

	urls := buildShortFeedLANURLs(interfaces, netip.Addr{}, 18088)

	if len(urls) != 1 || urls[0] != "http://192.168.162.250:18088/short/" {
		t.Fatalf("默认路由探测失败时仍应列出候选: %v", urls)
	}
}
