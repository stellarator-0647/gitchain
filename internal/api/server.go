// Package api exposes the node to the local browser. It binds to loopback,
// requires the per-node access token on every /api call and WebSocket, checks
// Origin against an allow list and bounds request sizes (docs/protocol.md §9.4).
package api

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/subtle"
	"errors"
	"io"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gin-gonic/gin"
	"github.com/gitchain/gitchain/internal/chain"
	"github.com/gitchain/gitchain/internal/node"
	"github.com/gitchain/gitchain/internal/protocol"
	"github.com/gitchain/gitchain/internal/verify"
)

// TokenHeader carries the local access token.
const TokenHeader = "X-GitChain-Token"

// Options configures the HTTP layer.
type Options struct {
	Token   string
	Origins []string // allowed browser origins, e.g. http://localhost:5173
	DevKey  *ecdsa.PrivateKey
	DevGov  chain.Governor // TEST key governance for scripts and the dev UI
}

// Server is the HTTP API around a node.
type Server struct {
	n    *node.Node
	hub  *Hub
	opts Options
}

// New builds the Gin engine.
func New(n *node.Node, hub *Hub, opts Options) *gin.Engine {
	s := &Server{n: n, hub: hub, opts: opts}
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery(), s.limitBody, s.cors)

	r.GET("/ws", func(c *gin.Context) {
		if !s.tokenOK(c.Query("token")) || !s.originOK(c.Request) {
			c.AbortWithStatus(http.StatusForbidden)
			return
		}
		hub.ServeWs(c.Writer, c.Request, s.originOK)
	})

	a := r.Group("/api", s.auth)
	a.GET("/status", func(c *gin.Context) { c.JSON(http.StatusOK, n.Status()) })
	a.GET("/chain/config", s.chainConfig)
	a.GET("/sync/status", func(c *gin.Context) { c.JSON(http.StatusOK, n.Status().Sync) })
	a.GET("/alerts", func(c *gin.Context) {
		st := n.Status()
		c.JSON(http.StatusOK, gin.H{"health": st.Health, "alerts": st.Alerts, "quarantine": st.Quarantine, "chainError": st.ChainError})
	})
	a.GET("/history", s.history)
	a.POST("/messages/draft", s.messageDraft)
	a.POST("/events/tombstone-draft", s.tombstoneDraft)
	a.POST("/events/commit", s.commit)
	a.DELETE("/events/draft/:id", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"released": n.CancelDraft(c.Param("id"))})
	})
	a.GET("/proof/:eventId", s.proof)
	a.POST("/delegations/draft", s.delegationDraft)
	a.POST("/delegations", s.setDelegation)
	a.DELETE("/delegations", func(c *gin.Context) {
		n.ClearDelegation()
		c.JSON(http.StatusOK, gin.H{"cleared": true})
	})
	a.GET("/proposals", func(c *gin.Context) {
		ps, err := n.Proposals(c.Request.Context())
		if err != nil {
			fail(c, http.StatusServiceUnavailable, "chain_unavailable", err)
			return
		}
		if ps == nil {
			ps = []chain.Proposal{}
		}
		c.JSON(http.StatusOK, ps)
	})
	a.GET("/orders", func(c *gin.Context) {
		os, err := n.OrderProposals(c.Request.Context())
		if err != nil {
			fail(c, http.StatusServiceUnavailable, "chain_unavailable", err)
			return
		}
		if os == nil {
			os = []chain.OrderProposal{}
		}
		c.JSON(http.StatusOK, os)
	})
	a.POST("/anchor/now", func(c *gin.Context) {
		skipped, err := n.AnchorNowReport(c.Request.Context())
		if err != nil {
			fail(c, http.StatusConflict, "anchor_failed", err)
			return
		}
		// skipped is "" when a checkpoint was submitted, else node.AnchorNothingNew / AnchorUnpushed.
		c.JSON(http.StatusOK, gin.H{"anchored": skipped == "", "skipped": skipped, "latest": n.Status().Latest})
	})
	a.GET("/git", func(c *gin.Context) {
		v, err := n.Git(40)
		if err != nil {
			fail(c, http.StatusInternalServerError, "git_failed", err)
			return
		}
		c.JSON(http.StatusOK, v)
	})
	a.POST("/sync/pause", func(c *gin.Context) {
		var req struct {
			Paused bool `json:"paused"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			fail(c, http.StatusBadRequest, "bad_request", err)
			return
		}
		n.SetSyncPaused(req.Paused)
		c.JSON(http.StatusOK, gin.H{"paused": req.Paused})
	})
	if opts.DevKey != nil {
		a.POST("/dev/send", s.devSend)
		a.POST("/dev/sign", s.devSign)
	}
	if opts.DevGov != nil {
		a.POST("/dev/propose", s.devPropose)
		a.POST("/dev/vote", s.devVote)
		a.POST("/dev/execute", s.devExecute)
		a.POST("/dev/order/propose", s.devProposeOrder)
		a.POST("/dev/order/vote", s.devVoteOrder)
	}
	return r
}

func fail(c *gin.Context, code int, kind string, err error) {
	c.AbortWithStatusJSON(code, gin.H{"error": kind, "detail": err.Error()})
}

func (s *Server) tokenOK(t string) bool {
	return s.opts.Token != "" && subtle.ConstantTimeCompare([]byte(t), []byte(s.opts.Token)) == 1
}

func (s *Server) originOK(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true // non-browser clients (curl, scripts) still need the token
	}
	for _, a := range s.opts.Origins {
		if o == a {
			return true
		}
	}
	return false
}

func (s *Server) auth(c *gin.Context) {
	if !s.originOK(c.Request) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "origin_not_allowed"})
		return
	}
	if !s.tokenOK(c.GetHeader(TokenHeader)) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing_or_bad_token"})
		return
	}
	c.Next()
}

func (s *Server) cors(c *gin.Context) {
	if o := c.GetHeader("Origin"); o != "" && s.originOK(c.Request) {
		h := c.Writer.Header()
		h.Set("Access-Control-Allow-Origin", o)
		h.Set("Vary", "Origin")
		h.Set("Access-Control-Allow-Headers", "Content-Type, "+TokenHeader)
		h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
	}
	if c.Request.Method == http.MethodOptions {
		c.AbortWithStatus(http.StatusNoContent)
		return
	}
	c.Next()
}

// limitBody bounds request size and rejects bodies that are not valid UTF-8,
// because encoding/json would otherwise silently replace bad bytes with
// U+FFFD and the user would sign text they never wrote.
func (s *Server) limitBody(c *gin.Context) {
	if c.Request.Body == nil || c.Request.Method == http.MethodGet {
		c.Next()
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, 64<<10))
	if err != nil {
		c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "body_too_large"})
		return
	}
	if !utf8.Valid(raw) {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": "body_not_utf8"})
		return
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(raw))
	c.Next()
}

func (s *Server) chainConfig(c *gin.Context) {
	cfg := s.n.Config()
	c.JSON(http.StatusOK, gin.H{"chainId": cfg.ChainID, "room": cfg.Room, "wallet": cfg.Wallet,
		"contracts": cfg.Contracts, "domain": gin.H{"name": s.n.Domain().Name, "version": s.n.Domain().Version}})
}

// history pages from the newest end: page 1 is the latest 50 events.
func (s *Server) history(c *gin.Context) {
	page, err := strconv.Atoi(c.DefaultQuery("page", "1"))
	if err != nil || page < 1 {
		page = 1
	}
	res := s.n.Result()
	if res == nil {
		c.JSON(http.StatusOK, gin.H{"total": 0, "page": page, "messages": []verify.Message{}})
		return
	}
	const size = 50
	total := len(res.Messages)
	end := total - (page-1)*size
	if end < 0 {
		end = 0
	}
	start := end - size
	if start < 0 {
		start = 0
	}
	c.JSON(http.StatusOK, gin.H{"total": total, "page": page, "head": res.Head, "messages": res.Messages[start:end]})
}

func (s *Server) messageDraft(c *gin.Context) {
	var req struct {
		Body string `json:"body"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "bad_request", err)
		return
	}
	d, err := s.n.PrepareMessage(req.Body)
	if err != nil {
		fail(c, http.StatusBadRequest, "draft_failed", err)
		return
	}
	c.JSON(http.StatusOK, d)
}

func (s *Server) tombstoneDraft(c *gin.Context) {
	var req struct {
		ProposalID uint64 `json:"proposalId"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "bad_request", err)
		return
	}
	d, err := s.n.PrepareTombstone(c.Request.Context(), req.ProposalID)
	if err != nil {
		fail(c, http.StatusConflict, "draft_failed", err)
		return
	}
	c.JSON(http.StatusOK, d)
}

func (s *Server) commit(c *gin.Context) {
	var req struct {
		DraftID string `json:"draftId"`
		Sig     string `json:"sig"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "bad_request", err)
		return
	}
	sig, err := hexutil.Decode(req.Sig)
	if err != nil {
		fail(c, http.StatusBadRequest, "bad_signature", err)
		return
	}
	id, err := s.n.CommitSigned(req.DraftID, sig)
	if err != nil {
		fail(c, http.StatusConflict, "commit_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"eventId": id, "state": "saved_locally"})
}

func (s *Server) proof(c *gin.Context) {
	id := common.HexToHash(c.Param("eventId"))
	pb, err := s.n.ExportProof(c.Request.Context(), id, c.Query("content") == "1")
	if err != nil {
		fail(c, http.StatusConflict, "proof_unavailable", err)
		return
	}
	c.JSON(http.StatusOK, pb)
}

func (s *Server) devSend(c *gin.Context) {
	var req struct {
		Body string `json:"body"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "bad_request", err)
		return
	}
	id, err := s.n.DevSend(req.Body, s.opts.DevKey)
	if err != nil {
		fail(c, http.StatusConflict, "send_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"eventId": id, "signedBy": "dev-test-key"})
}

func (s *Server) devSign(c *gin.Context) {
	var req struct {
		DraftID string `json:"draftId"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "bad_request", err)
		return
	}
	id, err := s.n.DevSignPending(req.DraftID, s.opts.DevKey)
	if err != nil {
		fail(c, http.StatusConflict, "sign_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"eventId": id, "signedBy": "dev-test-key"})
}

func (s *Server) devPropose(c *gin.Context) {
	var req struct {
		Target string `json:"target"`
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "bad_request", err)
		return
	}
	id, err := s.opts.DevGov.Propose(c.Request.Context(), s.n.Config().Room, common.HexToHash(req.Target), crypto.Keccak256Hash([]byte(req.Reason)))
	if err != nil {
		fail(c, http.StatusConflict, "propose_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"proposalId": id})
}

func (s *Server) devProposeOrder(c *gin.Context) {
	var req struct {
		A string `json:"a"`
		B string `json:"b"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "bad_request", err)
		return
	}
	id, err := s.opts.DevGov.ProposeOrder(c.Request.Context(), s.n.Config().Room, common.HexToHash(req.A), common.HexToHash(req.B))
	if err != nil {
		fail(c, http.StatusConflict, "propose_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"orderId": id})
}

func (s *Server) devVoteOrder(c *gin.Context) {
	var req struct {
		ID     uint64 `json:"id"`
		AFirst bool   `json:"aFirst"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "bad_request", err)
		return
	}
	if err := s.opts.DevGov.VoteOrder(c.Request.Context(), req.ID, req.AFirst); err != nil {
		fail(c, http.StatusConflict, "vote_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"voted": true})
}

func (s *Server) devVote(c *gin.Context) {
	var req struct {
		ID      uint64 `json:"id"`
		Support bool   `json:"support"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "bad_request", err)
		return
	}
	if err := s.opts.DevGov.Vote(c.Request.Context(), req.ID, req.Support); err != nil {
		fail(c, http.StatusConflict, "vote_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"voted": true})
}

func (s *Server) devExecute(c *gin.Context) {
	var req struct {
		ID uint64 `json:"id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "bad_request", err)
		return
	}
	if err := s.opts.DevGov.Execute(c.Request.Context(), req.ID); err != nil {
		fail(c, http.StatusConflict, "execute_failed", err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"executed": true})
}

func (s *Server) delegationDraft(c *gin.Context) {
	var req struct {
		Delegate string `json:"delegate"`
		Seconds  uint64 `json:"seconds"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || !common.IsHexAddress(req.Delegate) {
		fail(c, http.StatusBadRequest, "bad_request", errors.New("delegate must be an address"))
		return
	}
	d, err := s.n.PrepareDelegation(common.HexToAddress(req.Delegate), req.Seconds)
	if err != nil {
		fail(c, http.StatusBadRequest, "draft_failed", err)
		return
	}
	c.JSON(http.StatusOK, d)
}

func (s *Server) setDelegation(c *gin.Context) {
	var sd protocol.SignedDelegation
	if err := c.ShouldBindJSON(&sd); err != nil {
		fail(c, http.StatusBadRequest, "bad_request", err)
		return
	}
	a, err := s.n.SetDelegation(sd)
	if err != nil {
		fail(c, http.StatusConflict, "delegation_rejected", err)
		return
	}
	c.JSON(http.StatusOK, a)
}
