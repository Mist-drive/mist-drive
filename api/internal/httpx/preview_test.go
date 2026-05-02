package httpx

import (
	"image"
	"testing"
)

func TestFilePreviewType_Image(t *testing.T) {
	for _, ext := range []string{".jpg", ".png", ".gif", ".webp"} {
		if got := filePreviewType("file" + ext); got != "image" {
			t.Errorf("filePreviewType(%q) = %q, want %q", ext, got, "image")
		}
	}
}

func TestFilePreviewType_Text(t *testing.T) {
	for _, ext := range []string{".txt", ".md", ".go", ".json", ".ts"} {
		if got := filePreviewType("file" + ext); got != "text" {
			t.Errorf("filePreviewType(%q) = %q, want %q", ext, got, "text")
		}
	}
}

func TestFilePreviewType_Binary(t *testing.T) {
	for _, ext := range []string{".exe", ".zip", ".pdf"} {
		if got := filePreviewType("file" + ext); got != "" {
			t.Errorf("filePreviewType(%q) = %q, want empty string", ext, got)
		}
	}
}

func TestFilePreviewType_CaseInsensitive(t *testing.T) {
	if got := filePreviewType("photo.JPG"); got != "image" {
		t.Errorf("filePreviewType(.JPG) = %q, want image", got)
	}
	if got := filePreviewType("readme.TXT"); got != "text" {
		t.Errorf("filePreviewType(.TXT) = %q, want text", got)
	}
}

func TestSafeUTF8_ValidInput(t *testing.T) {
	input := "hello, world"
	if got := safeUTF8([]byte(input)); got != input {
		t.Fatalf("safeUTF8(%q) = %q, want unchanged", input, got)
	}
}

func TestSafeUTF8_TruncatesAtBoundary(t *testing.T) {
	// "é" is U+00E9, encoded as 0xC3 0xA9 in UTF-8 (2 bytes).
	// Build a slice that is valid UTF-8 up to some point but ends with
	// only the first byte of a 2-byte sequence.
	valid := []byte("hello ")
	partial := []byte{0xC3} // first byte of "é", no second byte
	b := append(valid, partial...)

	got := safeUTF8(b)
	if got != "hello " {
		t.Fatalf("safeUTF8 with partial rune: got %q, want %q", got, "hello ")
	}
}

func TestSafeUTF8_Empty(t *testing.T) {
	if got := safeUTF8([]byte{}); got != "" {
		t.Fatalf("safeUTF8(empty) = %q, want empty", got)
	}
	if got := safeUTF8(nil); got != "" {
		t.Fatalf("safeUTF8(nil) = %q, want empty", got)
	}
}

func TestThumbnailImage_NoScaleNeeded(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 100, 100))
	result := thumbnailImage(src, 200)
	b := result.Bounds()
	if b.Dx() != 100 || b.Dy() != 100 {
		t.Fatalf("expected 100x100, got %dx%d", b.Dx(), b.Dy())
	}
	// Must return the original image (no scaling needed).
	if result != src {
		t.Fatal("expected the original image to be returned unchanged")
	}
}

func TestThumbnailImage_ScaleDown(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 1600, 800))
	result := thumbnailImage(src, 800)
	b := result.Bounds()
	if b.Dx() > 800 {
		t.Fatalf("width %d exceeds maxDim 800", b.Dx())
	}
	if b.Dy() > 800 {
		t.Fatalf("height %d exceeds maxDim 800", b.Dy())
	}
	// Aspect ratio: original is 2:1 (1600x800). After scaling to fit 800px:
	// scale = 800/1600 = 0.5 → 800x400.
	if b.Dx() != 800 || b.Dy() != 400 {
		t.Fatalf("expected 800x400 (aspect preserved), got %dx%d", b.Dx(), b.Dy())
	}
}
