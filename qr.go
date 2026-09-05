package main

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"strconv"

	_ "embed"

	qrcode "github.com/skip2/go-qrcode"
	xdraw "golang.org/x/image/draw"
)

//go:embed logo.png
var logoPNG []byte

// qrWithLogo renders content as a QR PNG with the FERAL MODE mark centered.
// ECC is Highest (30% recoverable), and the logo plate covers ~6% of the
// symbol, so scanability is comfortably preserved.
func qrWithLogo(content string, size int) ([]byte, error) {
	q, err := qrcode.New(content, qrcode.Highest)
	if err != nil {
		return nil, err
	}
	qrImg := q.Image(size)

	out := image.NewRGBA(image.Rect(0, 0, size, size))
	xdraw.Draw(out, out.Bounds(), qrImg, image.Point{}, xdraw.Src)

	logo, err := png.Decode(bytes.NewReader(logoPNG))
	if err != nil {
		return nil, err
	}
	// White quiet plate slightly larger than the logo separates it from the
	// modules; the logo art itself is a black square with the red F.
	plate := size * 24 / 100
	logoSize := size * 20 / 100
	center := size / 2
	plateRect := image.Rect(center-plate/2, center-plate/2, center+plate/2, center+plate/2)
	xdraw.Draw(out, plateRect, image.White, image.Point{}, xdraw.Src)
	logoRect := image.Rect(center-logoSize/2, center-logoSize/2, center+logoSize/2, center+logoSize/2)
	xdraw.CatmullRom.Scale(out, logoRect, logo, logo.Bounds(), xdraw.Over, nil)

	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// handleLinkQR serves a QR PNG for a link's public short URL.
// GET /api/links/{code}/qr.png?size=1024
func (s *server) handleLinkQR(w http.ResponseWriter, r *http.Request) {
	code := normalizeCode(r.PathValue("code"))
	if _, err := s.store.GetLinkByCode(r.Context(), code); err != nil {
		writeError(w, http.StatusNotFound, "no such link")
		return
	}
	size := 1024
	if v, err := strconv.Atoi(r.URL.Query().Get("size")); err == nil && v >= 256 && v <= 2048 {
		size = v
	}
	pngBytes, err := qrWithLogo(s.cfg.publicBase+"/"+code, size)
	if err != nil {
		s.logger.Error("qr", "code", code, "err", err)
		writeError(w, http.StatusInternalServerError, "QR generation failed")
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Disposition", `inline; filename="feralmo-de-`+code+`.png"`)
	w.Write(pngBytes)
}
