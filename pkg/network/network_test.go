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
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/GoogleCloudPlatform/lustre-csi-driver/pkg/cloud_provider/metadata"
	"github.com/google/go-cmp/cmp"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// fakeLink is a mock implementation of netlink.Link for testing purposes.
type fakeLink struct {
	attrs *netlink.LinkAttrs
}

func (f fakeLink) Attrs() *netlink.LinkAttrs { return f.attrs }
func (f fakeLink) Type() string              { return "fakeLink" }

// fakeNodeClient is a fake implementation of the nodeClient interface.
type fakeNodeClient struct {
	// GetNodeWithRetry()
	getNodeResp *v1.Node
	getNodeErr  error
}

func (f *fakeNodeClient) GetNodeWithRetry(_ context.Context, _ string) (*v1.Node, error) {
	return f.getNodeResp, f.getNodeErr
}

// fakeNetlink is a fake implementation of the netlinker interface.
type fakeNetlink struct {
	// LinkList() Mock
	linkListResponse []netlink.Link
	linkListErr      error

	// LinkByName() Mock
	linkByNameResponse netlink.Link
	linkByNameMap      map[string]netlink.Link
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

	// GetStandardNICs() Mock
	getStandardNICsResponse []string
	getStandardNICsErr      error
}

func (f *fakeNetlink) LinkList() ([]netlink.Link, error) {
	return f.linkListResponse, f.linkListErr
}

func (f *fakeNetlink) LinkByName(name string) (netlink.Link, error) {
	if f.linkByNameErr != nil {
		return nil, f.linkByNameErr
	}
	if f.linkByNameMap != nil {
		if link, ok := f.linkByNameMap[name]; ok {
			return link, nil
		}

		return nil, fmt.Errorf("link %s not found", name)
	}

	return f.linkByNameResponse, nil
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

func (f *fakeNetlink) GetStandardNICs() ([]string, error) {
	return f.getStandardNICsResponse, f.getStandardNICsErr
}

// fakeMetadataClient is a fake implementation of the MetadataClient interface.
type fakeMetadataClient struct {
	// GetNetworkInterfaces() Mock
	getNicsResponse []metadata.NetworkInterface
	getNicsErr      error
}

func (f *fakeMetadataClient) GetNetworkInterfaces() ([]metadata.NetworkInterface, error) {
	return f.getNicsResponse, f.getNicsErr
}

func TestGetStandardNICs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		fakeGvnicNames []string
		fakeErr        error
		fakeNics       []metadata.NetworkInterface
		fakeMetaErr    error
		wantNics       []string
		wantErr        bool
	}{
		{
			name:           "Successfully get eth devices with correct drivers",
			fakeGvnicNames: []string{"eth0", "eth1"},
			fakeErr:        nil,
			fakeNics: []metadata.NetworkInterface{
				{Network: "projects/test/global/networks/default", Mac: "00:00:00:00:00:01"},
				{Network: "projects/test/global/networks/default", Mac: "00:00:00:00:00:02"},
			},
			wantNics: []string{"eth0", "eth1"},
			wantErr:  false,
		},
		{
			name:           "Filter out secondary NIC in different VPC",
			fakeGvnicNames: []string{"eth0", "eth1"},
			fakeErr:        nil,
			fakeNics: []metadata.NetworkInterface{
				{Network: "projects/test/global/networks/default", Mac: "00:00:00:00:00:01"},
				{Network: "projects/test/global/networks/other", Mac: "00:00:00:00:00:02"},
			},
			wantNics: []string{"eth0"}, // eth1 filtered out
			wantErr:  false,
		},
		{
			name:           "Filter out devices with wrong drivers",
			fakeGvnicNames: []string{"eth0"},
			fakeErr:        nil,
			wantNics:       []string{"eth0"},
			wantErr:        false,
		},
		{
			name:           "No eth devices",
			fakeGvnicNames: nil,
			fakeErr:        nil,
			wantNics:       nil,
			wantErr:        false,
		},
		{
			name:           "Netlink error",
			fakeGvnicNames: nil,
			fakeErr:        errors.New("failed to list links"),
			wantNics:       nil,
			wantErr:        true,
		},
		{
			name:           "Empty link list",
			fakeGvnicNames: nil,
			fakeErr:        nil,
			wantNics:       nil,
			wantErr:        false,
		},
		{
			name:           "Metadata error",
			fakeGvnicNames: []string{"eth0", "eth1"},
			fakeMetaErr:    errors.New("metadata failed"),
			wantErr:        true,
		},
		{
			name:           "NIC names not in alphabetical order",
			fakeGvnicNames: []string{"eth1", "eth0"},
			fakeErr:        nil,
			fakeNics: []metadata.NetworkInterface{
				{Network: "projects/test/global/networks/default", Mac: "00:00:00:00:00:01"},
				{Network: "projects/test/global/networks/default", Mac: "00:00:00:00:00:02"},
			},
			wantNics: []string{"eth0", "eth1"},
			wantErr:  false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fakeNL := &fakeNetlink{
				getStandardNICsResponse: test.fakeGvnicNames,
				getStandardNICsErr:      test.fakeErr,
				// Mock LinkByName to return MAC addresses.
				linkByNameMap: map[string]netlink.Link{
					"eth0": fakeLink{attrs: &netlink.LinkAttrs{Name: "eth0", HardwareAddr: net.HardwareAddr{0, 0, 0, 0, 0, 1}}},
					"eth1": fakeLink{attrs: &netlink.LinkAttrs{Name: "eth1", HardwareAddr: net.HardwareAddr{0, 0, 0, 0, 0, 2}}},
				},
			}

			fakeMeta := &fakeMetadataClient{
				getNicsResponse: test.fakeNics,
				getNicsErr:      test.fakeMetaErr,
			}

			networkManager := Manager(fakeNL, nil, fakeMeta)
			gotNics, err := networkManager.GetStandardNICs()
			if (err != nil) != test.wantErr {
				t.Errorf("GetStandardNICs() error = %v, wantErr: %v", err, test.wantErr)
			}
			if diff := cmp.Diff(test.wantNics, gotNics); diff != "" {
				t.Errorf("GetStandardNICs() returned unexpected diff (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGetNICIPAddr(t *testing.T) {
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

			networkManager := Manager(fakeNL, nil, nil)
			gotIP, err := networkManager.GetNICIPAddr(nicName)
			if (err != nil) != test.wantErr {
				t.Errorf("GetNICIPAddr(%s) error = %v, wantErr %v", nicName, err, test.wantErr)

				return
			}
			if !test.wantErr && !gotIP.Equal(wantIP) {
				t.Errorf("GetNICIPAddr(%s) = %v, want %v", nicName, gotIP, wantIP)
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
			name:               "Failure: GetNICIPAddr - LinkByName error",
			fakeLinkByNameResp: nil,
			fakeLinkByNameErr:  errors.New("link not found"),
			wantErr:            true,
			expectedErrSubstr:  "could not get link",
		},
		{
			name:               "Failure: GetNICIPAddr - AddrList error",
			fakeLinkByNameResp: linkResponse,
			fakeLinkByNameErr:  nil,
			fakeAddrListResp:   nil,
			fakeAddrListErr:    errors.New("failed to list addrs"),
			wantErr:            true,
			expectedErrSubstr:  "could not get address",
		},
		{
			name:               "Failure: GetNICIPAddr - AddrList returns empty",
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

			networkManager := Manager(fakeNL, nil, nil)
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

			networkManager := Manager(fakeNL, nil, nil)
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

func TestCheckDisableMultiNIC(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	nodeID := "test-node-1"

	tests := []struct {
		name              string
		nics              []string
		disableMultiNic   bool // This represents cluster level configuration.
		fakeNode          *v1.Node
		fakeNodeErr       error
		wantDisable       bool
		wantErr           bool
		expectedErrSubstr string
	}{
		{
			name:            "Single NIC - MultiNic disabled",
			nics:            []string{"eth0"},
			disableMultiNic: false, // Should be ignored
			wantDisable:     true,
			wantErr:         false,
		},
		{
			name:            "No NICs - MultiNic disabled",
			nics:            []string{},
			disableMultiNic: true, // Should be ignored
			wantDisable:     true,
			wantErr:         false,
		},
		{
			name:              "GetNodeWithRetry error",
			nics:              []string{"eth0", "eth1"},
			disableMultiNic:   false,
			fakeNodeErr:       errors.New("node not found"),
			wantDisable:       false,
			wantErr:           true,
			expectedErrSubstr: "node not found",
		},
		{
			name:            "Label not found - disableMultiNic is true",
			nics:            []string{"eth0", "eth1"},
			disableMultiNic: true,
			fakeNode:        &v1.Node{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"other-label": "value"}}},
			wantDisable:     true,
			wantErr:         false,
		},
		{
			name:            "Label not found - disableMultiNic is false",
			nics:            []string{"eth0", "eth1"},
			disableMultiNic: false,
			fakeNode:        &v1.Node{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"other-label": "value"}}},
			wantDisable:     false,
			wantErr:         false,
		},
		{
			name:            "Label found - multiRailLabel: true",
			nics:            []string{"eth0", "eth1"},
			disableMultiNic: true, // Should be overridden
			fakeNode:        &v1.Node{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{multiNICLabel: "true"}}},
			wantDisable:     false, // !true
			wantErr:         false,
		},
		{
			name:            "Label found - multiRailLabel: false",
			nics:            []string{"eth0", "eth1"},
			disableMultiNic: false, // Should be overridden
			fakeNode:        &v1.Node{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{multiNICLabel: "false"}}},
			wantDisable:     true,
			wantErr:         false,
		},
		{
			name:              "Label found - invalid bool value",
			nics:              []string{"eth0", "eth1"},
			disableMultiNic:   false,
			fakeNode:          &v1.Node{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{multiNICLabel: "invalid"}}},
			wantDisable:       false,
			wantErr:           true,
			expectedErrSubstr: `strconv.ParseBool: parsing "invalid": invalid syntax`,
		},
		{
			name:            "Alias found - multiRailLabel: true",
			nics:            []string{"eth0", "eth1"},
			disableMultiNic: true, // Should be overridden
			fakeNode:        &v1.Node{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{multiNICLabelAlias: "true"}}},
			wantDisable:     false, // !true
			wantErr:         false,
		},
		{
			name:            "Alias found - multiRailLabel: false",
			nics:            []string{"eth0", "eth1"},
			disableMultiNic: false, // Should be overridden
			fakeNode:        &v1.Node{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{multiNICLabelAlias: "false"}}},
			wantDisable:     true,
			wantErr:         false,
		},
		{
			name:            "Both labels found - multiNicLabel takes precedence",
			nics:            []string{"eth0", "eth1"},
			disableMultiNic: true,
			fakeNode: &v1.Node{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{
				multiNICLabel:      "true",
				multiNICLabelAlias: "false",
			}}},
			wantDisable: false, // multi-nic is true, so disable is false
			wantErr:     false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fakeNC := &fakeNodeClient{
				getNodeResp: test.fakeNode,
				getNodeErr:  test.fakeNodeErr,
			}
			rm := Manager(nil, fakeNC, nil)

			gotDisable, err := rm.CheckDisableMultiNIC(ctx, nodeID, test.nics, test.disableMultiNic)

			switch {
			case (err != nil) != test.wantErr:
				t.Errorf("CheckDisableMultiNIC(%v, %v, %v) got error = %v, wantErr %v", nodeID, test.nics, test.disableMultiNic, err, test.wantErr)
			case err == nil && test.wantErr == false:
				if gotDisable != test.wantDisable {
					t.Errorf("CheckDisableMultiNIC(%v, %v, %v) = %v, want %v", nodeID, test.nics, test.disableMultiNic, gotDisable, test.wantDisable)
				}
			case err != nil && test.expectedErrSubstr != "":
				if !strings.Contains(err.Error(), test.expectedErrSubstr) {
					t.Errorf("CheckDisableMultiNIC(%v, %v, %v) error = %v, should contain %q", nodeID, test.nics, test.disableMultiNic, err, test.expectedErrSubstr)
				}
			}
		})
	}
}
