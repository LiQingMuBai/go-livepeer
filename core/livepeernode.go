/*
Core contains the main functionality of the Livepeer node.

The logical orgnization of the `core` module is as follows:

livepeernode.go: Main struct definition and code that is common to all node types.
broadcaster.go: Code that is called only when the node is in broadcaster mode.
orchestrator.go: Code that is called only when the node is in orchestrator mode.

*/
package core

import (
	"context"
	"errors"
	"math/rand"
	"net/url"
	"sync"
	"time"

	"github.com/livepeer/go-livepeer/pm"

	"github.com/livepeer/go-livepeer/common"
	"github.com/livepeer/go-livepeer/eth"
	"github.com/livepeer/go-livepeer/ipfs"
	"github.com/livepeer/go-livepeer/net"
)

var ErrTranscoderAvail = errors.New("ErrTranscoderUnavailable")
var ErrLivepeerNode = errors.New("ErrLivepeerNode")
var ErrTranscode = errors.New("ErrTranscode")
var DefaultJobLength = int64(5760) //Avg 1 day in 15 sec blocks
var LivepeerVersion = "0.3.1-unstable"

type NodeType int

const (
	BroadcasterNode NodeType = iota
	OrchestratorNode
	TranscoderNode
)

//LivepeerNode handles videos going in and coming out of the Livepeer network.
type LivepeerNode struct {

	// Common fields
	Eth             eth.LivepeerEthClient
	EthEventMonitor eth.EventMonitor
	EthServices     map[string]eth.EventService
	WorkDir         string
	NodeType        NodeType
	Database        *common.DB

	// Transcoder public fields
	ClaimManagers    map[int64]eth.ClaimManager
	SegmentChans     map[ManifestID]SegmentChan
	Recipient        pm.Recipient
	PMSessions       map[ManifestID]map[string]bool
	OrchestratorPool net.OrchestratorPool
	Ipfs             ipfs.IpfsApi
	ServiceURI       *url.URL
	OrchSecret       string
	Transcoder       Transcoder

	// Transcoder private fields
	claimMutex      *sync.Mutex
	segmentMutex    *sync.Mutex
	pmSessionsMutex *sync.Mutex
	tcoderMutex     *sync.RWMutex
	taskMutex       *sync.RWMutex
	taskChans       map[int64]TranscoderChan
	taskCount       int64
}

//NewLivepeerNode creates a new Livepeer Node. Eth can be nil.
func NewLivepeerNode(e eth.LivepeerEthClient, wd string, dbh *common.DB) (*LivepeerNode, error) {
	rand.Seed(time.Now().UnixNano())
	return &LivepeerNode{
		Eth:             e,
		WorkDir:         wd,
		Database:        dbh,
		EthServices:     make(map[string]eth.EventService),
		ClaimManagers:   make(map[int64]eth.ClaimManager),
		SegmentChans:    make(map[ManifestID]SegmentChan),
		PMSessions:      make(map[ManifestID]map[string]bool),
		claimMutex:      &sync.Mutex{},
		segmentMutex:    &sync.Mutex{},
		pmSessionsMutex: &sync.Mutex{},
		tcoderMutex:     &sync.RWMutex{},
		taskMutex:       &sync.RWMutex{},
		taskChans:       make(map[int64]TranscoderChan),
	}, nil

}

func (n *LivepeerNode) StartEthServices() error {
	var err error
	for k, s := range n.EthServices {
		// Skip BlockService until the end
		if k == "BlockService" {
			continue
		}
		err = s.Start(context.Background())
		if err != nil {
			return err
		}
	}

	// Make sure to initialize BlockService last so other services can
	// create filters starting from the last seen block
	if s, ok := n.EthServices["BlockService"]; ok {
		if err := s.Start(context.Background()); err != nil {
			return err
		}
	}

	return nil
}

func (n *LivepeerNode) StopEthServices() error {
	var err error
	for _, s := range n.EthServices {
		err = s.Stop()
		if err != nil {
			return err
		}
	}

	return nil
}
