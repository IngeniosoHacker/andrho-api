package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/IngeniosoHacker/andrho-api/internal/brand"
	"github.com/IngeniosoHacker/andrho-api/internal/db"
	"github.com/IngeniosoHacker/andrho-api/internal/provision"
)

// What a business uploads (account_assets): its logo, images of its brand
// and its data from the previous system. The long signup uploads them right
// after creating the account; "Subir después" is the same endpoint from the
// panel. Images get their palette read (internal/brand); data gets mapped
// to Odoo (internal/intake) and, when the ERP is ready, loaded
// (provision.MigrateData). Creators see every file in the account's tasks.

const (
	maxImageBytes = 5 << 20
	maxDataBytes  = 15 << 20
)

var imageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/webp": true, "image/gif": true}

// UploadAsset handles POST /account/assets (editor+), multipart: kind
// (logo | brand_image | data) and file.
func (h *Handler) UploadAsset(c *gin.Context) {
	claims, _ := currentClaims(c)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxDataBytes+(1<<20))
	kind := c.PostForm("kind")
	fh, err := c.FormFile("file")
	if err != nil {
		respondError(c, http.StatusBadRequest, "falta el archivo (o pesa más de 15 MB)")
		return
	}
	limit := maxImageBytes
	if kind == "data" {
		limit = maxDataBytes
	} else if kind != "logo" && kind != "brand_image" {
		respondError(c, http.StatusBadRequest, "kind debe ser logo, brand_image o data")
		return
	}
	if fh.Size > int64(limit) {
		respondError(c, http.StatusBadRequest, "el archivo pesa más de "+map[bool]string{true: "15", false: "5"}[kind == "data"]+" MB")
		return
	}
	f, err := fh.Open()
	if err != nil {
		respondError(c, http.StatusBadRequest, "no pudimos leer el archivo")
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	f.Close()
	if err != nil || len(data) == 0 || len(data) > limit {
		respondError(c, http.StatusBadRequest, "no pudimos leer el archivo")
		return
	}
	ctx := c.Request.Context()
	if n, err := db.CountAssets(ctx, h.Accounts, claims.AccountID); err != nil || n >= db.MaxAssets {
		respondError(c, http.StatusBadRequest, "ya tienes el máximo de archivos; borra alguno antes")
		return
	}
	asset, status, msg := h.storeAsset(ctx, claims.AccountID, &claims.Sub, kind, fh.Filename, data)
	if status != 0 {
		respondError(c, status, msg)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"asset": asset})
}

// storeAsset validates, analyzes and stores one file, then sets off what it
// starts (the logo the site shows, the data task, the migration).
func (h *Handler) storeAsset(ctx context.Context, accountID string, by *string, kind, filename string, data []byte) (db.Asset, int, string) {
	filename = cleanFilename(filename)
	a := db.Asset{ID: uuid.NewString(), AccountID: accountID, Kind: kind, Filename: filename, Data: data, Status: "listo"}
	if kind == "data" {
		a.ContentType = "application/octet-stream"
		status, meta := provision.AnalyzeData(filename, data)
		a.Status = status
		a.Meta, _ = json.Marshal(meta)
	} else {
		ct := http.DetectContentType(data)
		if !imageTypes[ct] {
			return a, http.StatusBadRequest, "la imagen debe ser PNG, JPG, WebP o GIF"
		}
		a.ContentType = ct
		palette, err := brand.ImagePalette(data)
		if err != nil {
			return a, http.StatusBadRequest, "no pudimos leer la imagen"
		}
		a.Meta, _ = json.Marshal(provision.ImageMeta{Palette: palette})
	}
	a, err := db.CreateAsset(ctx, h.Accounts, a, by)
	if err != nil {
		return a, http.StatusInternalServerError, "internal error"
	}
	switch kind {
	case "logo":
		if a.ContentType != "image/gif" {
			_ = db.SetSetupLogo(ctx, h.Accounts, accountID, a.ContentType, data)
		}
	case "data":
		if pacc, err := h.ProvisionAccount(ctx, accountID); err == nil {
			_ = h.Provision.DataTask(ctx, pacc)
			if a.Status == provision.DataAnalyzed {
				go func() {
					mctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
					defer cancel()
					h.Provision.MigrateData(mctx, pacc)
				}()
			}
		}
	}
	a.Data = nil
	return a, 0, ""
}

var unsafeName = regexp.MustCompile(`[^\p{L}\p{N} ._()-]+`)

func cleanFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.TrimSpace(unsafeName.ReplaceAllString(name, "_"))
	for utf8.RuneCountInString(name) > 120 {
		r := []rune(name)
		name = string(r[len(r)-120:])
	}
	if name == "" || name == "." {
		name = "archivo"
	}
	return name
}

// kitOf reads an account's kit.
func (h *Handler) kitOf(ctx context.Context, accountID string) brand.Kit {
	var k brand.Kit
	if raw, err := db.GetKit(ctx, h.Accounts, accountID); err == nil {
		_ = json.Unmarshal(raw, &k)
	}
	return k
}

// ListAssets handles GET /account/assets: the files, the kit and the
// brand's palette.
func (h *Handler) ListAssets(c *gin.Context) {
	claims, _ := currentClaims(c)
	ctx := c.Request.Context()
	assets, err := db.ListAssets(ctx, h.Accounts, claims.AccountID)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	kit := h.kitOf(ctx, claims.AccountID)
	palette, source := provision.Palette(kit, assets)
	c.JSON(http.StatusOK, gin.H{"assets": assets, "kit": kit, "palette": palette, "palette_source": source})
}

// AssetFile handles GET /account/assets/:id/file: the file as uploaded.
func (h *Handler) AssetFile(c *gin.Context) {
	claims, _ := currentClaims(c)
	h.serveAsset(c, claims.AccountID, c.Param("id"))
}

func (h *Handler) serveAsset(c *gin.Context, accountID, id string) {
	a, err := db.GetAsset(c.Request.Context(), h.Accounts, accountID, id)
	if err != nil {
		respondError(c, http.StatusNotFound, "archivo no encontrado")
		return
	}
	disposition := "attachment"
	if imageTypes[a.ContentType] {
		disposition = "inline"
	}
	c.Header("Content-Disposition", disposition+`; filename*=UTF-8''`+urlPathEscape(a.Filename))
	c.Header("Cache-Control", "private, max-age=300")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, a.ContentType, a.Data)
}

func urlPathEscape(s string) string {
	var b strings.Builder
	for _, r := range []byte(s) {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.IndexByte("-._~", r) >= 0 {
			b.WriteByte(r)
		} else {
			b.WriteString("%" + strings.ToUpper(hex2(r)))
		}
	}
	return b.String()
}

func hex2(b byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[b>>4], digits[b&15]})
}

// DeleteAsset handles DELETE /account/assets/:id (admin+).
func (h *Handler) DeleteAsset(c *gin.Context) {
	claims, _ := currentClaims(c)
	if err := db.DeleteAsset(c.Request.Context(), h.Accounts, claims.AccountID, c.Param("id")); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			respondError(c, http.StatusNotFound, "archivo no encontrado")
			return
		}
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// UpdateKit handles PUT /account/kit (admin+): the palette and the choices,
// changed after signup ("Subir después", or another palette).
func (h *Handler) UpdateKit(c *gin.Context) {
	claims, _ := currentClaims(c)
	var req brand.Kit
	if err := c.ShouldBindJSON(&req); err != nil {
		respondError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	ctx := c.Request.Context()
	cur := h.kitOf(ctx, claims.AccountID)
	next := req.Sanitize()
	// Numbers aren't edited here.
	next.TeamSize, next.AccessUsers = cur.TeamSize, cur.AccessUsers
	raw, _ := json.Marshal(next)
	if err := db.SetKit(ctx, h.Accounts, claims.AccountID, raw); err != nil {
		respondError(c, http.StatusInternalServerError, "internal error")
		return
	}
	// Asking AndRho to make the logo or the colors now opens the task.
	if (next.Logo == "crear" && cur.Logo != "crear") || (next.Colors == "crear" && cur.Colors != "crear") {
		if pacc, err := h.ProvisionAccount(ctx, claims.AccountID); err == nil {
			setup, _ := db.GetAccountSetup(ctx, h.Accounts, claims.AccountID)
			_ = h.Provision.BrandTask(ctx, pacc, provision.Brief{Company: pacc.Company, ClientID: pacc.ClientID, Essence: setup.BrandEssence, Kit: &next})
		}
	}
	c.JSON(http.StatusOK, gin.H{"kit": next})
}

// paletteRequest is a signup preview: images as data URLs (the page shrinks
// them first).
type paletteRequest struct {
	Images []string `json:"images"`
}

var dataURLRe = regexp.MustCompile(`^data:image/(?:png|jpeg|webp|gif);base64,([A-Za-z0-9+/=\s]+)$`)

// Palette handles POST /analyzer/palette (public): the colors of up to 6
// images, merged, for the signup's "Generar desde mi logo" and the images
// of the colors card.
func (h *Handler) Palette(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 3<<20)
	var req paletteRequest
	if err := c.ShouldBindJSON(&req); err != nil || len(req.Images) == 0 || len(req.Images) > 6 {
		respondError(c, http.StatusBadRequest, "envía de 1 a 6 imágenes")
		return
	}
	var all [][]brand.Swatch
	for _, img := range req.Images {
		m := dataURLRe.FindStringSubmatch(img)
		if m == nil {
			respondError(c, http.StatusBadRequest, "las imágenes deben ser PNG, JPG, WebP o GIF")
			return
		}
		data, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(m[1]), ""))
		if err != nil {
			respondError(c, http.StatusBadRequest, "no pudimos leer una imagen")
			return
		}
		p, err := brand.ImagePalette(data)
		if err != nil {
			respondError(c, http.StatusBadRequest, "no pudimos leer una imagen")
			return
		}
		all = append(all, p)
	}
	c.JSON(http.StatusOK, gin.H{"palette": brand.Merge(all...), "images": all})
}

// CreatorTaskAsset handles GET /creators/tasks/:id/assets/:assetId: a file
// of the task's account, for the Creator working on it.
func (h *Handler) CreatorTaskAsset(c *gin.Context) {
	t, err := db.GetCreatorTask(c.Request.Context(), h.Accounts, c.Param("id"))
	if err != nil {
		respondError(c, http.StatusNotFound, "tarea no encontrada")
		return
	}
	h.serveAsset(c, t.AccountID, c.Param("assetId"))
}

// taskExtras is what a Creator sees with each task besides its brief: the
// account's files (which ones depend on the kind), its kit and palette, and
// the instructions.
type taskExtras struct {
	Assets        []db.Asset `json:"assets"`
	Kit           brand.Kit  `json:"kit"`
	Palette       []string   `json:"palette"`
	PaletteSource string     `json:"palette_source"`
	Instructions  []string   `json:"instructions"`
}

// assetsFor says which files go with a task: brand work gets the images,
// data work the data, a suggestion's task everything.
func assetsFor(kind string, all []db.Asset) []db.Asset {
	out := []db.Asset{}
	for _, a := range all {
		switch kind {
		case provision.KindData:
			if a.Kind != "data" {
				continue
			}
		case provision.KindBrand, "website":
			if a.Kind == "data" {
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

// withExtras adds taskExtras to tasks (one read per account).
func (h *Handler) withExtras(ctx context.Context, tasks []db.CreatorTask) []gin.H {
	type acct struct {
		assets  []db.Asset
		kit     brand.Kit
		essence string
	}
	cache := map[string]acct{}
	out := make([]gin.H, 0, len(tasks))
	for _, t := range tasks {
		a, ok := cache[t.AccountID]
		if !ok {
			a.assets, _ = db.ListAssets(ctx, h.Accounts, t.AccountID)
			a.kit = h.kitOf(ctx, t.AccountID)
			if s, err := db.GetAccountSetup(ctx, h.Accounts, t.AccountID); err == nil {
				a.essence = s.BrandEssence
			}
			cache[t.AccountID] = a
		}
		files := assetsFor(t.Kind, a.assets)
		palette, source := provision.Palette(a.kit, a.assets)
		ex := taskExtras{Assets: files, Kit: a.kit, Palette: palette, PaletteSource: source,
			Instructions: provision.Instructions(t.Kind, t.CompanyName, a.essence, a.kit, files)}
		raw, _ := json.Marshal(t)
		var m gin.H
		_ = json.Unmarshal(raw, &m)
		m["assets"], m["kit"], m["palette"], m["palette_source"], m["instructions"] = ex.Assets, ex.Kit, ex.Palette, ex.PaletteSource, ex.Instructions
		out = append(out, m)
	}
	return out
}
