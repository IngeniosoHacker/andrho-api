package handlers

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/IngeniosoHacker/andrho-api/internal/odoo"
)

// The ERP's side of the live sales: andrho_live_sale (andrho_odoo repo)
// keeps items, reservations in stock and shipping cards; these wrap its
// andrho_* methods.

const (
	liveModel  = "andrho.live.sale"
	lineModel  = "andrho.live.sale.line"
	cardModel  = "andrho.live.card"
	buyerModel = "andrho.live.buyer"
	liveModule = "andrho_live_sale"
)

// liveLine is one item of a live as the ERP describes it.
type liveLine struct {
	ID        int     `json:"id"`
	Sequence  int     `json:"sequence"`
	ProductID int     `json:"product_id"`
	Product   string  `json:"product"`
	LiveName  string  `json:"live_name"`
	Price     float64 `json:"price"`
	State     string  `json:"state"`
	Buyer     string  `json:"buyer"`
	BuyerPSID string  `json:"buyer_psid"`
	CardID    any     `json:"card_id"`
	PostID    string  `json:"post_id"`
	CommentID string  `json:"comment_id"`
}

// liveCard is a shipping card in a live's summary.
type liveCard struct {
	ID          int     `json:"id"`
	Buyer       string  `json:"buyer"`
	FirstName   string  `json:"first_name"`
	Phone       string  `json:"phone"`
	Address     string  `json:"address"`
	Zone        string  `json:"zone"`
	Items       int     `json:"items"`
	Total       float64 `json:"total"`
	DiscountPct float64 `json:"discount_pct"`
	State       string  `json:"state"`
}

// liveSummary is andrho.live.sale.andrho_summary().
type liveSummary struct {
	ID             int        `json:"id"`
	Name           string     `json:"name"`
	State          string     `json:"state"`
	Channel        string     `json:"channel"`
	Date           string     `json:"date"`
	LiveURL        string     `json:"live_url"`
	PlatformLiveID string     `json:"platform_live_id"`
	StartedAt      string     `json:"started_at"`
	EndedAt        string     `json:"ended_at"`
	ItemCount      int        `json:"item_count"`
	SoldCount      int        `json:"sold_count"`
	ReservedCount  int        `json:"reserved_count"`
	UnsoldCount    int        `json:"unsold_count"`
	Revenue        float64    `json:"revenue"`
	BuyerCount     int        `json:"buyer_count"`
	Lines          []liveLine `json:"lines"`
	Cards          []liveCard `json:"cards"`
}

func erpLiveCreate(ctx context.Context, cl *odoo.Client, name string, items []map[string]any) (int, error) {
	var id int
	err := cl.Exec(ctx, liveModel, "andrho_create", []any{name, items}, nil, &id)
	return id, err
}

func erpLiveSetOrder(ctx context.Context, cl *odoo.Client, liveID int, ordered []map[string]any) error {
	return cl.Exec(ctx, liveModel, "andrho_set_order", []any{[]int{liveID}, ordered}, nil, nil)
}

func erpLiveSummary(ctx context.Context, cl *odoo.Client, liveID int) (liveSummary, error) {
	var s liveSummary
	err := cl.Exec(ctx, liveModel, "andrho_summary", []any{[]int{liveID}}, nil, &s)
	return s, err
}

func erpLiveStart(ctx context.Context, cl *odoo.Client, liveID int, url, platformID string) error {
	return cl.Exec(ctx, liveModel, "andrho_start", []any{[]int{liveID}, url, platformID}, nil, nil)
}

func erpLiveFinish(ctx context.Context, cl *odoo.Client, liveID int) error {
	return cl.Exec(ctx, liveModel, "andrho_finish", []any{[]int{liveID}}, nil, nil)
}

func erpLines(ctx context.Context, cl *odoo.Client, ids []int) ([]liveLine, error) {
	var out []liveLine
	err := cl.Exec(ctx, lineModel, "andrho_lines", []any{ids}, nil, &out)
	return out, err
}

func erpLine(ctx context.Context, cl *odoo.Client, id int) (liveLine, error) {
	ls, err := erpLines(ctx, cl, []int{id})
	if err != nil {
		return liveLine{}, err
	}
	if len(ls) == 0 {
		return liveLine{}, fmt.Errorf("live line %d not found", id)
	}
	return ls[0], nil
}

// erpReserve gives the item to the commenter; ok=false when it was taken.
func erpReserve(ctx context.Context, cl *odoo.Client, lineID int, authorID, name, pageID, commentID, text string) (bool, error) {
	var res any
	err := cl.Exec(ctx, lineModel, "andrho_reserve", []any{[]int{lineID}, authorID, name, pageID, commentID, text}, nil, &res)
	if err != nil {
		return false, err
	}
	b, isBool := res.(bool)
	return !isBool || b, nil
}

func erpRelease(ctx context.Context, cl *odoo.Client, lineID int) error {
	return cl.Exec(ctx, lineModel, "andrho_release", []any{[]int{lineID}}, nil, nil)
}

// confirmResult is andrho_confirm's answer.
type confirmResult struct {
	CardID      int     `json:"card_id"`
	NeedsData   bool    `json:"needs_data"`
	DiscountPct float64 `json:"discount_pct"`
	Phone       string  `json:"phone"`
	Address     string  `json:"address"`
	Zone        string  `json:"zone"`
}

func erpConfirm(ctx context.Context, cl *odoo.Client, lineID int) (confirmResult, error) {
	var r confirmResult
	err := cl.Exec(ctx, lineModel, "andrho_confirm", []any{[]int{lineID}}, nil, &r)
	return r, err
}

func erpSetShipping(ctx context.Context, cl *odoo.Client, cardID int, phone, address, zone string) error {
	kw := map[string]any{}
	if phone != "" {
		kw["phone"] = phone
	}
	if address != "" {
		kw["address"] = address
	}
	if zone != "" {
		kw["zone"] = zone
	}
	return cl.Exec(ctx, cardModel, "andrho_set_shipping", []any{[]int{cardID}}, kw, nil)
}

func erpMarkPosted(ctx context.Context, cl *odoo.Client, lineID int, postID string) error {
	return cl.Exec(ctx, lineModel, "andrho_mark_posted", []any{[]int{lineID}, postID}, nil, nil)
}

// erpPostData is the photo (decoded) and sales description of an item.
func erpPostData(ctx context.Context, cl *odoo.Client, lineID int) ([]byte, string, error) {
	var d struct {
		Image       string `json:"image"`
		Description string `json:"description"`
	}
	if err := cl.Exec(ctx, lineModel, "andrho_post_data", []any{[]int{lineID}}, nil, &d); err != nil {
		return nil, "", err
	}
	if d.Image == "" {
		return nil, d.Description, nil
	}
	img, err := base64.StdEncoding.DecodeString(strings.TrimSpace(d.Image))
	return img, d.Description, err
}

// Settings the seller changes from AndRho: they live in the ERP's
// ir.config_parameter, read by the cards and the loyalty program.
var liveParams = map[string]string{
	"card_message":     "andrho.live.card_message",
	"card_show_logo":   "andrho.live.card_show_logo",
	"loyalty_enabled":  "andrho.live.loyalty_enabled",
	"loyalty_min":      "andrho.live.loyalty_min_purchases",
	"loyalty_discount": "andrho.live.loyalty_discount",
	"claim_word":       "andrho.live.claim_word",
}

func erpGetParam(ctx context.Context, cl *odoo.Client, key string) (string, error) {
	var v any
	err := cl.Exec(ctx, "ir.config_parameter", "get_param", []any{key}, nil, &v)
	s, _ := v.(string)
	return s, err
}

func erpSetParam(ctx context.Context, cl *odoo.Client, key, value string) error {
	return cl.Exec(ctx, "ir.config_parameter", "set_param", []any{key, value}, nil, nil)
}
