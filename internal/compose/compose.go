// Package compose holds the client-side message-construction helpers shared by
// the flag commands and the console: minimal RFC 5322 text messages and ULID
// delivery ids. Moved verbatim from internal/cli when the console grew a
// compose form (cli imports tui, so tui cannot import cli).
package compose

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"mime"
	"strings"
	"time"
)

// NewDeliveryID mints a fresh ULID for use as an X-Delivery-Id: a 48-bit
// millisecond timestamp + 80 random bits, Crockford base32, 26 chars,
// lexicographically sortable. It is well within core's validateDeliveryId
// length bound and never uses a reserved prefix (sent:/bounce:) or a '#'.
// Reuse the same id across retries of one logical send so idempotent
// redelivery converges.
func NewDeliveryID() string {
	var id [16]byte
	ms := uint64(time.Now().UnixMilli())
	id[0] = byte(ms >> 40)
	id[1] = byte(ms >> 32)
	id[2] = byte(ms >> 24)
	id[3] = byte(ms >> 16)
	id[4] = byte(ms >> 8)
	id[5] = byte(ms)
	_, _ = rand.Read(id[6:])
	return encodeULID(id)
}

// Crockford is the ULID alphabet (exported for shape tests).
const Crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// encodeULID renders 16 bytes as a canonical 26-char Crockford-base32 ULID.
func encodeULID(id [16]byte) string {
	dst := make([]byte, 26)
	dst[0] = Crockford[(id[0]&224)>>5]
	dst[1] = Crockford[id[0]&31]
	dst[2] = Crockford[(id[1]&248)>>3]
	dst[3] = Crockford[((id[1]&7)<<2)|((id[2]&192)>>6)]
	dst[4] = Crockford[(id[2]&62)>>1]
	dst[5] = Crockford[((id[2]&1)<<4)|((id[3]&240)>>4)]
	dst[6] = Crockford[((id[3]&15)<<1)|((id[4]&128)>>7)]
	dst[7] = Crockford[(id[4]&124)>>2]
	dst[8] = Crockford[((id[4]&3)<<3)|((id[5]&224)>>5)]
	dst[9] = Crockford[id[5]&31]
	dst[10] = Crockford[(id[6]&248)>>3]
	dst[11] = Crockford[((id[6]&7)<<2)|((id[7]&192)>>6)]
	dst[12] = Crockford[(id[7]&62)>>1]
	dst[13] = Crockford[((id[7]&1)<<4)|((id[8]&240)>>4)]
	dst[14] = Crockford[((id[8]&15)<<1)|((id[9]&128)>>7)]
	dst[15] = Crockford[(id[9]&124)>>2]
	dst[16] = Crockford[((id[9]&3)<<3)|((id[10]&224)>>5)]
	dst[17] = Crockford[id[10]&31]
	dst[18] = Crockford[(id[11]&248)>>3]
	dst[19] = Crockford[((id[11]&7)<<2)|((id[12]&192)>>6)]
	dst[20] = Crockford[(id[12]&62)>>1]
	dst[21] = Crockford[((id[12]&1)<<4)|((id[13]&240)>>4)]
	dst[22] = Crockford[((id[13]&15)<<1)|((id[14]&128)>>7)]
	dst[23] = Crockford[(id[14]&124)>>2]
	dst[24] = Crockford[((id[14]&3)<<3)|((id[15]&224)>>5)]
	dst[25] = Crockford[id[15]&31]
	return string(dst)
}

// TextMessage builds a minimal RFC 5322 text/plain message.
func TextMessage(from, to, subject, body string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	if subject != "" {
		fmt.Fprintf(&b, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	}
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "Message-ID: <%s@%s>\r\n", NewDeliveryID(), hostFromAddress(from))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	// Normalize line endings to CRLF.
	body = strings.ReplaceAll(body, "\r\n", "\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\r\n")
	}
	return b.Bytes()
}

func hostFromAddress(addr string) string {
	if i := strings.LastIndex(addr, "@"); i >= 0 && i < len(addr)-1 {
		return addr[i+1:]
	}
	return "openemail.local"
}
