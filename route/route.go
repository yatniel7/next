// Package route handle route table for darwin/linux.
package route

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net"
	"strings"
	"time"

	"github.com/chzyer/flow"
	"github.com/chzyer/logex"
	"github.com/chzyer/next/ip"
	"github.com/chzyer/next/util"
)

var (
	ErrRouteItemNotFound = logex.Define("route item '%v' not found")
	ErrRouteItemExists   = logex.Define("route item '%v' is exists")
	ErrRouteItemContains = logex.Define("route item '%v' contains by '%v'")
)

// one line "CIDR\tCOMMENT"
type Item struct {
	CIDR    string
	Comment string
	IPNet   *net.IPNet
}

func NewItemCIDR(cidr string, comment string) (*Item, error) {
	_, ipnet, err := net.ParseCIDR(FormatCIDR(cidr))
	if err != nil {
		return nil, err
	}
	return NewItem(ipnet, comment), nil
}

func NewItem(ipnet *net.IPNet, comment string) *Item {
	return &Item{
		CIDR:    ipnet.String(),
		Comment: comment,
		IPNet:   ipnet,
	}
}

func (i Item) Match(target *net.IPNet) bool {
	return ip.MatchIPNet(target, i.IPNet)
}

func (i Item) String() string {
	return fmt.Sprintf("%v\t%v", i.CIDR, i.Comment)
}

type Route struct {
	flow             *flow.Flow
	items            *Items
	ephemeralItems   *EphemeralItems
	devName          string
	newEphemeralItem chan struct{}
}

func NewRoute(f *flow.Flow, devName string) *Route {
	r := &Route{
		flow:             f,
		devName:          devName,
		items:            &Items{},
		ephemeralItems:   NewEphemeralItems(),
		newEphemeralItem: make(chan struct{}, 1),
	}
	go r.loop()
	return r
}

func (r *Route) GetEphemeralItems() []EphemeralItem {
	ret := make([]EphemeralItem, 0, r.ephemeralItems.Len())
	for elem := r.ephemeralItems.list.Front(); elem != nil; elem = elem.Next() {
		ei := elem.Value.(*EphemeralItem)
		ret = append(ret, *ei)
	}
	return ret
}

func (r *Route) GetItems() Items {
	return *r.items
}

func (r *Route) loop() {
loop:
	for {
		i := r.ephemeralItems.GetFront()
		if i == nil {
			select {
			case <-r.newEphemeralItem:
			case <-r.flow.IsClose():
				break loop
			}
		} else {
			now := time.Now()
			if now.After(i.Expired) {
				logex.Infof("route '%v' is expired", i.CIDR)
				err := r.RemoveEphemeralItem(i.CIDR)
				if err != nil {
					logex.Error("remove route item fail:", err.Error())
				}
			} else {
				select {
				case <-time.After(i.Expired.Sub(now)):
				case <-r.newEphemeralItem:
				case <-r.flow.IsClose():
					break loop
				}
			}
		}
	}
}

func (r *Route) RemoveItem(cidr string) error {
	if item := r.items.Remove(cidr); item != nil {
		return r.DeleteRoute(cidr)
	}
	if err := r.RemoveEphemeralItem(cidr); err != nil {
		return err
	}
	return ErrRouteItemNotFound.Format(cidr)
}

func (r *Route) RemoveEphemeralItem(cidr string) error {
	if r.ephemeralItems.Remove(cidr) != nil {
		return logex.Trace(r.DeleteRoute(cidr))
	}
	return ErrRouteItemNotFound.Format(cidr)
}

func (r *Route) PersistEphemeralItem(cidr string) error {
	if ei := r.ephemeralItems.Remove(cidr); ei != nil {
		r.items.Append(ei.Item)
		r.items.Sort()
		return nil
	}
	return ErrRouteItemNotFound.Format(cidr)
}

func (r *Route) AddEphemeralItem(i *EphemeralItem) error {
	if err := checkValidCIDR(i.CIDR); err != nil {
		return err
	}

	r.ephemeralItems.Add(i)
	select {
	case r.newEphemeralItem <- struct{}{}:
	default:
	}
	return logex.Trace(r.SetRoute(i.CIDR))
}

func (r *Route) Match(ipnet *net.IPNet) *Item {
	if item := r.ephemeralItems.Match(ipnet); item != nil {
		return item.Item
	}
	if item := r.items.Match(ipnet); item != nil {
		return item
	}
	return nil
}

func (r *Route) AddItem(i *Item) error {
	if item := r.Match(i.IPNet); item != nil {
		return ErrRouteItemContains.Format(i.CIDR, item.CIDR)
	}
	r.items.Append(i)
	r.items.Sort()
	return logex.Trace(r.SetRoute(i.CIDR))
}

func (r *Route) DeleteRoute(cidr string) error {
	sh := genRemoveRouteCmd(cidr)
	return logex.Trace(util.Shell(sh))
}

func (r *Route) SetRoute(cidr string) error {
	sh := genAddRouteCmd(r.devName, cidr)
	return logex.Trace(util.Shell(sh))
}

func (r *Route) Load(fp string) error {
	rule, err := ioutil.ReadFile(fp)
	if err != nil {
		return logex.Trace(err)
	}
	reader := bytes.NewBuffer(rule)
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			cmd := strings.TrimSpace(string(line))
			sp := strings.Split(cmd, "\t")
			cidr, comment := sp[0], ""
			if len(sp) >= 2 {
				comment = sp[1]
			}
			item, err := NewItemCIDR(cidr, comment)
			if err != nil {
				logex.Error(err)
				continue
			}
			if err := r.AddItem(item); err != nil {
				logex.Error("add item", cidr, "fail:", err.Error())
			}
		}
		if err != nil {
			break
		}
	}
	r.items.Sort()

	return nil
}

func (r *Route) Save(fp string) error {
	buf := bytes.NewBuffer(nil)
	for _, item := range *r.items {
		fmt.Fprintln(buf, item)
	}
	return logex.Trace(ioutil.WriteFile(fp, buf.Bytes(), 0644))
}

func FormatCIDR(cidr string) string {
	if idx := strings.Index(cidr, "/"); idx < 0 {
		cidr += "/32"
	}

	_, ipnet, err := net.ParseCIDR(cidr)
	if err == nil {
		cidr = ipnet.String()
	}

	return cidr
}

func checkValidCIDR(cidr string) error {
	_, _, err := net.ParseCIDR(cidr)
	if err != nil {
		err = fmt.Errorf("invalid CIDR: %v", err.Error())
	}
	return err
}
