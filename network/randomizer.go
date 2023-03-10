package network

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	sm "github.com/filecoin-project/go-filecoin/actor/builtin/storagemarket"
	types "github.com/filecoin-project/go-filecoin/types"

	randfile "github.com/filecoin-project/filecoin-network-sim/randfile"
)

type Action int

const (
	ActionPayment Action = iota
	ActionAsk
	ActionBid
	ActionDeal
	ActionSendFile
)

type Args struct {
	StartNodes      int
	MaxNodes        int
	ForkBranching   int
	ForkProbability float64
	JoinTime        time.Duration
	BlockTime       time.Duration
	ActionTime      time.Duration
	TestfilesDir    string
	Actions         ActionArgs
}

type ActionArgs struct {
	Ask     bool
	Bid     bool
	Deal    bool
	Payment bool
	Mine    bool
}

type Randomizer struct {
	Net     *Network
	Args    Args
	Actions []Action
}

func NewRandomizer(n *Network, a Args) *Randomizer {
	r := &Randomizer{
		Net:     n,
		Args:    a,
		Actions: []Action{},
	}

	addif := func(t bool, a Action) {
		if t {
			r.Actions = append(r.Actions, a)
		}
	}
	addif(a.Actions.Ask, ActionAsk)
	addif(a.Actions.Bid, ActionBid)
	addif(a.Actions.Deal, ActionDeal)
	addif(a.Actions.Payment, ActionPayment)

	return r
}

func (r *Randomizer) Run(ctx context.Context) {
	time.Sleep(time.Millisecond * 100) // output correctly
	fmt.Println("\nRandomizer running with params:")
	fmt.Println(StructToString(&r.Args))

	if r.Args.Actions.Mine {
		go r.mineBlocks(ctx)
	}
	go r.addAndRemoveNodes(ctx)
	go r.randomActions(ctx)
}

func (r *Randomizer) periodic(ctx context.Context, t time.Duration, periodicFunc func(ctx context.Context)) {
	for {
		time.Sleep(t)

		select {
		case <-ctx.Done():
			return
		default:
		}

		periodicFunc(ctx)
	}
}

func nextRandomType(n *Network) NodeType {
	c := n.GetNodeCounts()
	if float64(c[ClientNodeType]) < (float64(c[MinerNodeType]) * 1.5) {
		return ClientNodeType
	}

	return AnyNodeType
}

func (r *Randomizer) addInitialNodes(ctx context.Context) {
	fmt.Printf("starting with %d nodes\n", r.Args.StartNodes)
	var wg sync.WaitGroup
	for i := 0; i < r.Args.StartNodes; i++ {
		t := ClientNodeType
		if i%2 == 0 {
			t = MinerNodeType
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Net.AddNode(t)
		}()
	}
	wg.Wait()

	// ensure all nodes are connected. (async start)
	for _, n := range r.Net.GetNodesOfType(AnyNodeType) {
		if n == nil {
			logErr(fmt.Errorf("node is nil: %v", n))
			continue
		}

		r.Net.ConnectNodeToAll(n)
	}
}

func (r *Randomizer) addAndRemoveNodes(ctx context.Context) {
	// add nodes at the beginning, to get going faster
	r.addInitialNodes(ctx)

	// periodically add more nodes
	r.periodic(ctx, r.Args.JoinTime, func(ctx context.Context) {
		if r.Net.Size() >= r.Args.MaxNodes {
			return
		}

		t := nextRandomType(r.Net)
		_, err := r.Net.AddNode(t)
		logErr(err)
	})
}

func rollToMine(probability float64) bool {
	if probability < 0.001 {
		return false
	}
	if probability > 0.999 {
		return true
	}

	roll := rand.Float64()
	// fmt.Printf("probability roll: %f < %f\n", roll, probability)
	return roll < probability
}

// Only miners should mine block
func (r *Randomizer) mineBlocks(ctx context.Context) {
	fmt.Println("mining automatically")

	epoch := -1 // so next one is 0.
	r.periodic(ctx, r.Args.BlockTime, func(ctx context.Context) {
		epoch++

		// once per ForkBranching.
		// do it this way, to sample without replacement and deal with the case
		// where there are (N < ForkBranching) nodes in the network.
		nds := r.Net.GetRandomNodes(MinerNodeType, r.Args.ForkBranching)
		// fmt.Printf("epoch %d: %d to mine\n", epoch, len(nds))
		var wg sync.WaitGroup
		for _, n := range nds {
			if rollToMine(r.Args.ForkProbability) {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_, err := n.Daemon.MiningOnce(context.TODO())
					logErr(err)
				}()
			}
		}
		wg.Wait()
	})
}

func (r *Randomizer) randomActions(ctx context.Context) {
	r.periodic(ctx, r.Args.ActionTime, func(ctx context.Context) {
		if len(r.Actions) < 1 {
			return
		}

		action := r.Actions[rand.Intn(len(r.Actions))]
		go r.doRandomAction(ctx, action)
	})
}

func (r *Randomizer) doRandomAction(ctx context.Context, a Action) {
	switch a {
	case ActionPayment:
		r.doActionPayment(ctx)
	case ActionAsk:
		r.doActionAsk(ctx)
	case ActionBid:
		r.doActionBid(ctx)
	case ActionDeal:
		r.doActionDeal(ctx)
	case ActionSendFile:
	}
}

func (r *Randomizer) doActionPayment(ctx context.Context) {
	var amtToSend = 5000

	nds := r.Net.GetRandomNodes(AnyNodeType, 2)
	if len(nds) < 2 || nds[0] == nil || nds[1] == nil {
		log.Print("[RAND]\t not enough nodes for random actions")
		return
	}

	log.Print("[RAND]\t Trying to send payment.")
	a1, err1 := FilecoinGetMainWalletAddress(ctx, nds[0].Daemon)
	a2, err2 := FilecoinGetMainWalletAddress(ctx, nds[1].Daemon)
	logErr(err1)
	logErr(err2)
	if a1.String() == "" || a2.String() == "" {
		log.Print("[RAND]\t could not get wallet addresses.", a1, a2, err1, err2)
		return
	}

	// ensure source has balance first. if doesn't, it wont work.
	bal, err := nds[0].Daemon.WalletBalance(ctx, a1)
	if err != nil {
		log.Print("[RAND]\t could not get balance for address: ", a1)
		return
	}
	if bal.LessThan(types.NewAttoFILFromFIL(uint64(amtToSend))) {
		log.Printf("[RAND]\t not enough money in address: %s %d", a1, bal)
		return
	}

	// if does not succeed in 3 block times, it's hung on an error
	ctx, _ = context.WithTimeout(ctx, r.Args.BlockTime*3)
	logErr(SendFilecoin(ctx, nds[0].Daemon, a1, a2, amtToSend))
	return
}

func (r *Randomizer) doActionAsk(ctx context.Context) {
	size := (rand.Intn(16) + 1) + 30 // ~MB
	price := rand.Intn(13) + 13

	nd := r.Net.GetRandomNode(MinerNodeType)
	if nd == nil {
		return
	}

	// ensure they have a miner addrss associated with them.
	from, err := nd.CreateOrGetMinerIdentity()
	if err != nil {
		logErr(err)
		return
	}

	log.Printf("adding ask: %s %d %d", from, size, price)
	logErr(nd.Daemon.MinerAddAsk(ctx, from, size, price))
	return
}

func (r *Randomizer) doActionBid(ctx context.Context) {
	// size := (rand.Intn(16) + 1) * (1 << 20) // ~MB
	size := (rand.Intn(16) + 1) + 30
	price := rand.Intn(17) + 1

	nd := r.Net.GetRandomNode(ClientNodeType)
	if nd == nil {
		return
	}

	// ensure they have an addr they can bid from
	from, err := nd.Daemon.GetMainWalletAddress()
	if err != nil {
		logErr(err)
		return
	}

	log.Printf("adding bid: %s %d %d", from, size, price)
	logErr(nd.Daemon.ClientAddBid(ctx, from, size, price))
	return
}

func (r *Randomizer) doActionDeal(ctx context.Context) {
	nd := r.Net.GetRandomNode(ClientNodeType)
	if nd == nil {
		return
	}

	/*
		from, err := nd.Daemon.GetMainWalletAddress()
		if err != nil {
			logErr(err)
			return
		}
	*/

	out, err := nd.Daemon.OrderbookGetAsks(ctx)
	if err != nil {
		logErr(err)
		return
	}
	asks, err := extractAsks(out.ReadStdout())
	if err != nil {
		logErr(err)
		return
	}

	out, err = nd.Daemon.OrderbookGetBids(ctx)
	if err != nil {
		logErr(err)
		return
	}

	bids, err := extractUnusedBids(out.ReadStdout())
	if err != nil {
		logErr(err)
		return
	}

	wallet := nd.WalletAddr

	log.Printf("Wallet Address: %s\n", wallet)

	ask, bid, err := getBestDealPair(asks, bids, wallet)

	if err != nil {
		logErr(err)
		return
	}

	log.Printf("[RAND] deal found ask and bid\n")
	log.Printf("[RAND] ask %d %s %s\n", ask.ID, ask.Price.String(), ask.Size.String())
	log.Printf("[RAND] bid %d %s %s\n", bid.ID, bid.Price.String(), bid.Size.String())

	// get a randomfile
	fp, err := randfile.RandomFile(r.Args.TestfilesDir, nd.RepoDir)
	if err != nil {
		logErr(err)
		return
	}

	out, err = nd.Daemon.ClientImport(fp)
	if err != nil {
		logErr(err)
		return
	}

	cid := out.ReadStdoutTrimNewlines()

	out, err = nd.Daemon.ProposeDeal(ask.ID, bid.ID, cid)
	if err != nil {
		logErr(err)
		return
	}

	log.Printf("[RAND] deal proposal: %s\n", out)
}

func getBestDealPair(asks []sm.Ask, bids []sm.Bid, wallet string) (sm.Ask, sm.Bid, error) {
	// Sort bids by ID, FIFO
	sort.Slice(bids[:], func(i, j int) bool {
		return bids[i].ID < bids[j].ID
	})

	// Sort asks by Price, as we will always want the best price
	sort.Slice(asks[:], func(i, j int) bool {
		return asks[i].Price.LessThan(asks[j].Price)
	})

	// All bids for the given wallet
	var walletBids []sm.Bid

	// Find all bids for the provided wallet
	for _, b := range bids {
		if b.Owner.String() == wallet {
			log.Printf("[RAND] getBestDealPair found bid for wallet %s id, %d, price %s, size, %s\n", wallet, b.ID, b.Price.String(), b.Size.String())
			walletBids = append(walletBids, b)
		}
	}

	if len(walletBids) != 0 {
		for _, b := range walletBids {
			// Check to see if the bid fits within an ask
			for _, a := range asks {
				if b.Size.LessEqual(a.Size) && b.Price.GreaterEqual(a.Price) {
					// Valid bid for ask
					return a, b, nil
				}
			}
		}
	} else {
		log.Printf("Could not find any bids for wallet %s\n", wallet)
	}

	err := fmt.Errorf("Could not find matching ask / bid for wallet %s", wallet)
	return sm.Ask{}, sm.Bid{}, err

}

func extractAsks(input string) ([]sm.Ask, error) {

	// remove last new line
	o := strings.Trim(input, "\n")
	// separate ndjson on new lines
	as := strings.Split(o, "\n")
	log.Printf("[RAND] extractAsks: asks of length %d: %v\n", len(as), as)
	if len(as) <= 1 {
		return nil, fmt.Errorf("No Asks yet")
	}

	var asks []sm.Ask
	for _, a := range as {
		var ask sm.Ask
		log.Printf("[RAND] extractAsks: ask %v\n", a)
		err := json.Unmarshal([]byte(a), &ask)
		if err != nil {
			panic(err)
		}
		asks = append(asks, ask)
	}
	return asks, nil
}

func extractUnusedBids(input string) ([]sm.Bid, error) {
	// remove last new line
	o := strings.Trim(input, "\n")
	// separate ndjson on new lines
	bs := strings.Split(o, "\n")
	log.Printf("[RAND] extractUnusedBids: bids of length %d: %v\n", len(bs), bs)
	if len(bs) <= 1 {
		return nil, fmt.Errorf("No Bids yet")
	}

	var bids []sm.Bid
	for _, b := range bs {
		var bid sm.Bid
		log.Printf("[RAND] extractUnusedBids: bid %v\n", b)
		err := json.Unmarshal([]byte(b), &bid)
		if err != nil {
			panic(err)
		}
		if bid.Used {
			continue
		}
		bids = append(bids, bid)
	}
	return bids, nil
}

func extractDeals(input string) []sm.Deal {

	// remove last new line
	o := strings.Trim(input, "\n")
	// separate ndjson on new lines
	ds := strings.Split(o, "\n")
	log.Printf("[RAND] extractDeals: deals of length %d: %v\n", len(ds), ds)

	var deals []sm.Deal
	for _, d := range ds {
		var deal sm.Deal
		log.Printf("[RAND] extractDeals: deal %v\n", d)
		err := json.Unmarshal([]byte(d), &deal)
		if err != nil {
			panic(err)
		}
		deals = append(deals, deal)
	}
	return deals
}

func logErr(err error) {
	if err != nil {
		log.Printf("[RAND]\t ERROR: %s\n", err.Error())
	}
}

func StructToString(v interface{}) string {
	return StructToStringIndent(v, 0)
}

func StructToStringIndent(vi interface{}, indent int) string {
	tabStr := strings.Repeat("\t", indent)
	str := ""
	t := reflect.TypeOf(vi).Elem()
	v := reflect.ValueOf(vi).Elem()
	for i := 0; i < t.NumField(); i++ {
		fv := v.Field(i)
		str += tabStr + t.Field(i).Name + ":"
		if t.Field(i).Type.Kind() == reflect.Struct {
			str += "\n" + StructToStringIndent(fv.Addr().Interface(), indent+1) + "\n"
		} else {
			str += fmt.Sprintf(" %v\n", fv.Interface())
		}
	}
	return str
}
