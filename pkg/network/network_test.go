/*
Copyright 2025 Google LLC

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package network

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// fakeLink is a mock implementation of netlink.Link for testing purposes.
type fakeLink struct {
	attrs *netlink.LinkAttrs
}

func (f fakeLink) Attrs() *netlink.LinkAttrs { return f.attrs }
func (f fakeLink) Type() string              { return "fakeLink" }

// fakeNetlink is a fake implementation of the netlinker interface.
type fakeNetlink struct {
	// LinkList() Mock
	linkListResponse []netlink.Link
	linkListErr      error

	// LinkByName() Mock
	linkByNameResponse netlink.Link
	linkByNameErr      error

	// AddrList() Mock
	addrListResponse []netlink.Addr
	addrListErr      error

	// RouteList() Mock
	routeListResponse []netlink.Route
	routeListErr      error

	// RouteAdd() and RouteReplace() error Mocks
	routeReplaceErr error
	ruleAddErr      error

	// RuleList() Mock
	ruleListResponse []netlink.Rule
	ruleListErr      error

	// RouteListFiltered() Mock
	routeListFilteredResponse map[int][]netlink.Route
	routeListFilteredErr      error
}

func (f *fakeNetlink) LinkList() ([]netlink.Link, error) {
	return f.linkListResponse, f.linkListErr
}

func (f *fakeNetlink) LinkByName(_ string) (netlink.Link, error) {
	return f.linkByNameResponse, f.linkByNameErr
}

func (f *fakeNetlink) AddrList(_ netlink.Link, _ int) ([]netlink.Addr, error) {
	return f.addrListResponse, f.addrListErr
}

func (f *fakeNetlink) RouteList(_ netlink.Link, _ int) ([]netlink.Route, error) {
	return f.routeListResponse, f.routeListErr
}

func (f *fakeNetlink) RouteReplace(_ *netlink.Route) error {
	return f.routeReplaceErr
}

func (f *fakeNetlink) RuleAdd(_ *netlink.Rule) error {
	return f.ruleAddErr
}

func (f *fakeNetlink) RuleList(_ int) ([]netlink.Rule, error) {
	return f.ruleListResponse, f.ruleListErr
}

func (f *fakeNetlink) RouteListFiltered(_ int, filter *netlink.Route, mask uint64) ([]netlink.Route, error) {
	if f.routeListFilteredErr != nil {
		return nil, f.routeListFilteredErr
	}
	if mask == netlink.RT_FILTER_TABLE && filter != nil {
		if routes, exists := f.routeListFilteredResponse[filter.Table]; exists {
			return routes, nil
		}
	}

	return []netlink.Route{}, nil
}

func TestGetGvnicNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		fakeLinks []netlink.Link
		fakeErr   error
		wantNics  []string
		wantErr   bool
	}{
		{
			name: "Successfully get eth devices",
			fakeLinks: []netlink.Link{
				fakeLink{attrs: &netlink.LinkAttrs{Name: "eth0"}},
				fakeLink{attrs: &netlink.LinkAttrs{Name: "eth1"}},
			},
			fakeErr:  nil,
			wantNics: []string{"eth0", "eth1"},
			wantErr:  false,
		},
		{
			name: "No eth devices",
			fakeLinks: []netlink.Link{
				fakeLink{attrs: &netlink.LinkAttrs{Name: "lo"}},
				fakeLink{attrs: &netlink.LinkAttrs{Name: "wlan0"}},
			},
			fakeErr:  nil,
			wantNics: nil,
			wantErr:  false,
		},
		{
			name:      "Netlink error",
			fakeLinks: nil,
			fakeErr:   errors.New("failed to list links"),
			wantNics:  nil,
			wantErr:   true,
		},
		{
			name:      "Empty link list",
			fakeLinks: []netlink.Link{},
			fakeErr:   nil,
			wantNics:  nil,
			wantErr:   false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fakeNL := &fakeNetlink{
				linkListResponse: test.fakeLinks,
				linkListErr:      test.fakeErr,
			}

			networkManager := Manager(fakeNL)
			gotNics, err := networkManager.GetGvnicNames()
			if (err != nil) != test.wantErr {
				t.Errorf("GetGvnicNames() error = %v, wantErr: %v", err, test.wantErr)
			}
			if diff := cmp.Diff(test.wantNics, gotNics); diff != "" {
				t.Errorf("GetGvnicNames() returned unexpected diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGetNicIPAddr(t *testing.T) {
	t.Parallel()

	wantIP := net.ParseIP("192.168.1.10")
	nicName := "eth0"
	ipNet := &net.IPNet{
		IP:   wantIP,
		Mask: net.CIDRMask(32, 32),
	}
	tests := []struct {
		name               string
		fakeLinkByNameResp netlink.Link
		fakeLinkByNameErr  error
		fakeAddrListResp   []netlink.Addr
		fakeAddrListErr    error
		wantErr            bool
	}{
		{
			name:               "Successfully get IP",
			fakeLinkByNameResp: fakeLink{attrs: &netlink.LinkAttrs{Name: "eth0"}},
			fakeLinkByNameErr:  nil,
			fakeAddrListResp:   []netlink.Addr{{IPNet: ipNet}},
			fakeAddrListErr:    nil,
			wantErr:            false,
		},
		{
			name:              "LinkByName error",
			fakeLinkByNameErr: errors.New("link not found"),
			wantErr:           true,
		},
		{
			name:               "AddrList error",
			fakeLinkByNameResp: fakeLink{attrs: &netlink.LinkAttrs{Name: "eth0"}},
			fakeAddrListErr:    errors.New("failed to list addrs"),
			wantErr:            true,
		},
		{
			name:               "AddrList returns empty",
			fakeLinkByNameResp: fakeLink{attrs: &netlink.LinkAttrs{Name: "eth0"}},
			fakeAddrListResp:   []netlink.Addr{},
			wantErr:            true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fakeNL := &fakeNetlink{
				linkByNameResponse: test.fakeLinkByNameResp,
				linkByNameErr:      test.fakeLinkByNameErr,
				addrListResponse:   test.fakeAddrListResp,
				addrListErr:        test.fakeAddrListErr,
			}

			networkManager := Manager(fakeNL)
			gotIP, err := networkManager.GetNicIPAddr(nicName)
			if (err != nil) != test.wantErr {
				t.Errorf("GetNicIPAddr(%s) error = %v, wantErr %v", nicName, err, test.wantErr)

				return
			}
			if !test.wantErr && !gotIP.Equal(wantIP) {
				t.Errorf("GetNicIPAddr(%s) = %v, want %v", nicName, gotIP, wantIP)
			}
		})
	}
}

func TestConfigureRoute(t *testing.T) {
	t.Parallel()

	nic := "eth0"
	nicIP := net.ParseIP("192.168.1.10")
	lustreInstanceIP := "10.0.0.1"
	tableID := 110
	linkResponse := fakeLink{attrs: &netlink.LinkAttrs{Name: nic, Index: 1}}
	addrListResp := []netlink.Addr{{IPNet: &net.IPNet{IP: nicIP, Mask: net.CIDRMask(32, 32)}}}
	routeListResp := []netlink.Route{{Gw: net.ParseIP("192.168.1.1")}}
	tests := []struct {
		name                string
		fakeLinkByNameResp  netlink.Link
		fakeLinkByNameErr   error
		fakeAddrListResp    []netlink.Addr
		fakeAddrListErr     error
		fakeRouteListResp   []netlink.Route
		fakeRouteListErr    error
		fakeRouteReplaceErr error
		fakeRuleAddErr      error
		wantErr             bool
		expectedErrSubstr   string
	}{
		{
			name:               "Successful ConfigureRoute for NIC into a table ID for lustre instance",
			fakeLinkByNameResp: linkResponse,
			fakeAddrListResp:   addrListResp,
			fakeRouteListResp:  routeListResp,
			wantErr:            false,
		},
		{
			name:               "Failure: GetNicIPAddr - LinkByName error",
			fakeLinkByNameResp: nil,
			fakeLinkByNameErr:  errors.New("link not found"),
			wantErr:            true,
			expectedErrSubstr:  "could not get link",
		},
		{
			name:               "Failure: GetNicIPAddr - AddrList error",
			fakeLinkByNameResp: linkResponse,
			fakeLinkByNameErr:  nil,
			fakeAddrListResp:   nil,
			fakeAddrListErr:    errors.New("failed to list addrs"),
			wantErr:            true,
			expectedErrSubstr:  "could not get address",
		},
		{
			name:               "Failure: GetNicIPAddr - AddrList returns empty",
			fakeLinkByNameResp: linkResponse,
			fakeLinkByNameErr:  nil,
			fakeAddrListResp:   []netlink.Addr{},
			fakeAddrListErr:    nil,
			wantErr:            true,
			expectedErrSubstr:  "could not get address",
		},
		{
			name:               "Failure: configureRoutesForNic - RouteList error",
			fakeLinkByNameResp: linkResponse,
			fakeLinkByNameErr:  nil,
			fakeAddrListResp:   addrListResp,
			fakeAddrListErr:    nil,
			fakeRouteListResp:  nil,
			fakeRouteListErr:   errors.New("failed to list routes"),
			wantErr:            true,
			expectedErrSubstr:  "failed to list routes",
		},
		{
			name:               "Failure: configureRoutesForNic - No gateway found",
			fakeLinkByNameResp: linkResponse,
			fakeLinkByNameErr:  nil,
			fakeAddrListResp:   addrListResp,
			fakeAddrListErr:    nil,
			fakeRouteListResp:  []netlink.Route{{}},
			fakeRouteListErr:   nil,
			wantErr:            true,
			expectedErrSubstr:  "could not find gateway",
		},
		{
			name:                "Failure: configureRoutesForNic - RouteReplace error",
			fakeLinkByNameResp:  linkResponse,
			fakeLinkByNameErr:   nil,
			fakeAddrListResp:    addrListResp,
			fakeAddrListErr:     nil,
			fakeRouteListResp:   routeListResp,
			fakeRouteListErr:    nil,
			fakeRouteReplaceErr: errors.New("failed to replace route"),
			wantErr:             true,
			expectedErrSubstr:   "failed to replace/add route",
		},
		{
			name:                "Failure: configureRoutesForNic - RuleAdd error",
			fakeLinkByNameResp:  linkResponse,
			fakeLinkByNameErr:   nil,
			fakeAddrListResp:    addrListResp,
			fakeAddrListErr:     nil,
			fakeRouteListResp:   routeListResp,
			fakeRouteListErr:    nil,
			fakeRouteReplaceErr: nil,
			fakeRuleAddErr:      errors.New("failed to add rule"),
			wantErr:             true,
			expectedErrSubstr:   "failed to add rule",
		},
		{
			name:                "Success: configureRoutesForNic - RuleAdd exists",
			fakeLinkByNameResp:  linkResponse,
			fakeLinkByNameErr:   nil,
			fakeAddrListResp:    addrListResp,
			fakeAddrListErr:     nil,
			fakeRouteListResp:   routeListResp,
			fakeRouteListErr:    nil,
			fakeRouteReplaceErr: nil,
			fakeRuleAddErr:      os.NewSyscallError("file exists", unix.EEXIST),
			wantErr:             false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fakeNL := &fakeNetlink{
				linkByNameResponse: test.fakeLinkByNameResp,
				linkByNameErr:      test.fakeLinkByNameErr,
				addrListResponse:   test.fakeAddrListResp,
				addrListErr:        test.fakeAddrListErr,
				routeListResponse:  test.fakeRouteListResp,
				routeListErr:       test.fakeRouteListErr,
				routeReplaceErr:    test.fakeRouteReplaceErr,
				ruleAddErr:         test.fakeRuleAddErr,
			}

			networkManager := Manager(fakeNL)
			err := networkManager.ConfigureRoute(nic, lustreInstanceIP, tableID)
			if (err != nil) != test.wantErr {
				t.Errorf("ConfigureRoute(%s, %s, %d) error = %v, wantErr %v", nic, lustreInstanceIP, tableID, err, test.wantErr)
			} else if err != nil && test.expectedErrSubstr != "" {
				if !strings.Contains(err.Error(), test.expectedErrSubstr) {
					t.Errorf("ConfigureRoute(%s, %s, %d) error = %v, should contain error message: %q", nic, lustreInstanceIP, tableID, err, test.expectedErrSubstr)
				}
			}
		})
	}
}

func TestFindNextFreeTableID(t *testing.T) {
	t.Parallel()

	nicIP := net.ParseIP("192.168.1.10")
	otherIP := net.ParseIP("192.168.1.11")

	tests := []struct {
		name                      string
		startID                   int
		fakeRuleListResp          []netlink.Rule
		fakeRuleListErr           error
		fakeRouteListFilteredResp map[int][]netlink.Route
		fakeRouteListFilteredErr  error
		wantID                    int
		wantErr                   bool
		expectedErrSubstr         string
	}{
		{
			name:                      "First ID is free",
			startID:                   100,
			fakeRuleListResp:          []netlink.Rule{},
			fakeRouteListFilteredResp: map[int][]netlink.Route{100: {}},
			wantID:                    100,
			wantErr:                   false,
		},
		{
			name:    "Finds free ID after occupied tables",
			startID: 100,
			fakeRuleListResp: []netlink.Rule{
				{Table: 100, Src: &net.IPNet{IP: otherIP, Mask: net.CIDRMask(32, 32)}}, // Table 100 occupied by different IP
			},
			fakeRouteListFilteredResp: map[int][]netlink.Route{
				101: {{LinkIndex: 1}}, // Table 101 occupied by a route
				102: {},               // Table 102 is free
			},
			wantID:  102,
			wantErr: false,
		},
		{
			name:    "Reuses table if rule exists for same IP",
			startID: 100,
			fakeRuleListResp: []netlink.Rule{
				{Table: 100, Src: &net.IPNet{IP: nicIP, Mask: net.CIDRMask(32, 32)}},
			},
			fakeRouteListFilteredResp: map[int][]netlink.Route{
				100: {{LinkIndex: 1}},
			},
			wantID:  100,
			wantErr: false,
		},
		{
			name:              "RuleList error",
			startID:           100,
			fakeRuleListErr:   errors.New("rule list failed"),
			wantErr:           true,
			expectedErrSubstr: "failed to list rules",
		},
		{
			name:    "RouteListFiltered error",
			startID: 100,
			fakeRuleListResp: []netlink.Rule{
				{Table: 100, Src: &net.IPNet{IP: otherIP, Mask: net.CIDRMask(32, 32)}},
			},
			fakeRouteListFilteredErr: errors.New("route list filtered failed"),
			wantErr:                  true,
			expectedErrSubstr:        "failed to list routes",
		},
		{
			name:    "No free tables found",
			startID: maxTableID - 1, // Start at 254 and we occupy 254 & 255
			fakeRuleListResp: []netlink.Rule{
				{Table: maxTableID - 1, Src: &net.IPNet{IP: otherIP}},
				{Table: maxTableID, Src: &net.IPNet{IP: otherIP}},
			},
			fakeRouteListFilteredResp: map[int][]netlink.Route{},
			wantID:                    0,
			wantErr:                   true,
			expectedErrSubstr:         fmt.Sprintf("no free routing tables found between %d and %d", maxTableID-1, maxTableID),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fakeNL := &fakeNetlink{
				ruleListResponse:          test.fakeRuleListResp,
				ruleListErr:               test.fakeRuleListErr,
				routeListFilteredResponse: test.fakeRouteListFilteredResp,
				routeListFilteredErr:      test.fakeRouteListFilteredErr,
			}

			networkManager := Manager(fakeNL)
			gotID, err := networkManager.FindNextFreeTableID(test.startID, nicIP)
			switch {
			case (err != nil) != test.wantErr:
				t.Errorf("FindNextFreeTableID(%d, %v) error = %v, wantErr %v", test.startID, nicIP, err, test.wantErr)
			case err == nil && test.wantErr == false:
				if gotID != test.wantID {
					t.Errorf("FindNextFreeTableID(%d, %v) = %d, want %d", test.startID, nicIP, gotID, test.wantID)
				}
			case err != nil && test.expectedErrSubstr != "":
				if !strings.Contains(err.Error(), test.expectedErrSubstr) {
					t.Errorf("FindNextFreeTableID(%d, %v) error = %v, should contain %q", test.startID, nicIP, err, test.expectedErrSubstr)
				}
			}
		})
	}
}
