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
	// Scale the FM mark to ~22% of the symbol width, keeping its aspect
	// ratio, and give it a white quiet border to separate it from the
	// modules. The covered area stays well under ECC H's 30% budget.
	lb := logo.Bounds()
	logoW := size * 22 / 100
	logoH := logoW * lb.Dy() / lb.Dx()
	pad := size * 2 / 100
	center := size / 2
	plateRect := image.Rect(center-logoW/2-pad, center-logoH/2-pad, center+logoW/2+pad, center+logoH/2+pad)
	xdraw.Draw(out, plateRect, image.White, image.Point{}, xdraw.Src)
	logoRect := image.Rect(center-logoW/2, center-logoH/2, center+logoW/2, center+logoH/2)
	xdraw.CatmullRom.Scale(out, logoRect, logo, lb, xdraw.Over, nil)

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
