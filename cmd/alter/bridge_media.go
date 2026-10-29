package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/verdu/alter/internal/adapter/telegram"
	"github.com/verdu/alter/internal/vault"
)

// Inbound files (S4, S6): the owner sends a document or a photo over Telegram,
// the bridge stores it inside the vault and hands Pi a prompt it can act on. The
// file never leaves the vault root and the name always travels through the vault
// store, which sanitises it: nothing in this file builds a path of its own.
const (
	// bridgeInboundMaxBytes is the inbound attachment ceiling: the Bot API cloud
	// download limit (S8). It is enforced twice, before the download on the size
	// Telegram declares and again by the vault store while streaming, because a
	// declared size can be missing or wrong.
	bridgeInboundMaxBytes int64 = 20 << 20

	// bridgeFallbackName names an attachment Telegram sent without one, so the
	// store and the prompt never talk about an empty file name.
	bridgeFallbackName = "file"

	// bridgeMediaTooLargeDeclaredReply explains a refusal decided before the
	// download, and names both sizes so the owner knows what to send instead.
	bridgeMediaTooLargeDeclaredReply = "No he descargado «%s»: Telegram dice que ocupa %s y el límite es %s. Envíalo más pequeño o compártelo como enlace."
	// bridgeMediaTooLargeStoreReply explains a refusal decided while streaming,
	// which is the only gate that catches a missing or lying declared size.
	bridgeMediaTooLargeStoreReply = "No he guardado «%s»: supera el límite de %s. Envíalo más pequeño o compártelo como enlace."
	// bridgeMediaFailedReply is the human line for a download that broke. The
	// cause belongs in the log; the chat gets something it can act on.
	bridgeMediaFailedReply = "No he podido guardar el archivo. Inténtalo de nuevo en un momento."
)

// inboundMedia is the outcome of storing one inbound attachment: either the
// vault-relative path of the stored file, or a human line explaining why nothing
// was stored plus the technical cause for the log.
type inboundMedia struct {
	// RelPath is the vault-relative path of the stored file, exactly what Pi
	// needs with cwd = vault root.
	RelPath string
	// Refusal is the human line for the chat when the file was not stored.
	Refusal string
	// Err is the cause behind a Refusal. It is logged, never sent.
	Err error
}

// handleMedia stores an inbound document or photo and runs one turn on it, so
// the owner gets the same feedback as with text: typing, drafts and a final
// answer. The caption, when present, is the instruction; without one Pi is told
// to acknowledge the file and ask one short question about it. A refusal only
// answers the chat: it never becomes an error, because one rejected attachment
// must never stop the channel.
func (r *bridgeRuntime) handleMedia(ctx context.Context, chatID int64, ev telegram.Event) error {
	name := inboundName(ev)

	stored := r.storeInbound(ctx, ev)
	if stored.Refusal != "" {
		r.logger.Printf("bridge: inbound %s %q not stored: %v", ev.Kind, name, stored.Err)
		return r.reply(ctx, chatID, stored.Refusal)
	}
	r.logger.Printf("bridge: stored inbound %s %q at %s", ev.Kind, name, stored.RelPath)

	return r.runTurn(ctx, chatID, bridgeMediaPrompt(stored.RelPath, name, ev.MimeType, ev.Caption))
}

// storeInbound resolves a document or photo and streams it into the vault,
// returning the vault-relative path so the caller can log or reuse it.
func (r *bridgeRuntime) storeInbound(ctx context.Context, ev telegram.Event) inboundMedia {
	name := inboundName(ev)

	// A declared oversize is refused before touching the network: downloading it
	// only to delete it wastes the owner's bandwidth and the Bot API quota.
	if ev.FileSize > r.inboundMax {
		return inboundMedia{
			Refusal: fmt.Sprintf(bridgeMediaTooLargeDeclaredReply,
				name, humanBytes(ev.FileSize), humanBytes(r.inboundMax)),
			Err: fmt.Errorf("declared size %d exceeds the %d byte limit", ev.FileSize, r.inboundMax),
		}
	}

	serverPath, err := r.client.GetFile(ctx, ev.FileID)
	if err != nil {
		return inboundMedia{Refusal: bridgeMediaFailedReply, Err: fmt.Errorf("getFile %q: %w", ev.FileID, err)}
	}

	// The download streams into the store through a pipe: neither the attachment
	// nor the size cap has to be buffered in memory, and the store sees exactly
	// the bytes Telegram sent.
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		derr := r.client.DownloadFile(ctx, serverPath, pw)
		// Closing with the error hands it to the store's reader, so a broken
		// transfer can never look like a short but complete file.
		_ = pw.CloseWithError(derr)
		done <- derr
	}()

	stored, err := r.store.SaveInbound(name, pr)
	// The store stops reading as soon as it decides (too large, write error), so
	// the read side is closed to release a download still in flight.
	_ = pr.CloseWithError(errors.New("bridge: attachment already decided"))
	dlErr := <-done

	switch {
	case errors.Is(err, vault.ErrTooLarge):
		return inboundMedia{
			Refusal: fmt.Sprintf(bridgeMediaTooLargeStoreReply, name, humanBytes(r.inboundMax)),
			Err:     err,
		}
	case err != nil:
		return inboundMedia{Refusal: bridgeMediaFailedReply, Err: fmt.Errorf("store %q: %w", name, err)}
	case dlErr != nil:
		// Defensive: a transfer that ended badly must never be handed to Pi as a
		// complete file.
		return inboundMedia{Refusal: bridgeMediaFailedReply, Err: fmt.Errorf("download %q: %w", name, dlErr)}
	}

	rel, relErr := filepath.Rel(r.store.Root(), stored)
	if relErr != nil {
		return inboundMedia{Refusal: bridgeMediaFailedReply, Err: fmt.Errorf("vault path of %q: %w", stored, relErr)}
	}
	return inboundMedia{RelPath: filepath.ToSlash(rel)}
}

// bridgeMediaPrompt builds the prompt that hands Pi one stored attachment. Pi
// runs with cwd = vault root, so the vault-relative path is what its read-only
// tools need; the prompt says so explicitly, because nothing else tells the
// model that a file arrived or where it went.
func bridgeMediaPrompt(relPath, fileName, mimeType, caption string) string {
	var b strings.Builder
	b.WriteString("El dueño te ha enviado un archivo por Telegram y lo he guardado en el vault.\n\n")
	fmt.Fprintf(&b, "- Ruta en el vault: %s\n", relPath)
	fmt.Fprintf(&b, "- Nombre original: %s\n", fileName)
	if mimeType = strings.TrimSpace(mimeType); mimeType != "" {
		fmt.Fprintf(&b, "- Tipo MIME: %s\n", mimeType)
	}
	b.WriteString("\nPuedes abrirlo con tus herramientas de lectura (read, grep, find, ls) usando esa ruta relativa: tu directorio de trabajo es la raíz del vault. Léelo y actúa sobre él.\n")

	if caption = strings.TrimSpace(caption); caption != "" {
		b.WriteString("\nInstrucción del dueño sobre el archivo:\n")
		b.WriteString(caption)
		b.WriteString("\n")
		return b.String()
	}
	b.WriteString("\nNo ha escrito ninguna instrucción: confirma en una línea que lo has recibido y hazle una sola pregunta corta sobre qué quiere que hagas con él.\n")
	return b.String()
}

// inboundName is the name Telegram declared, falling back to a neutral one. It is
// passed to the store unsanitised on purpose: sanitising is the store's job and
// doing it twice would make the stored name depend on this file.
func inboundName(ev telegram.Event) string {
	if name := strings.TrimSpace(ev.FileName); name != "" {
		return name
	}
	return bridgeFallbackName
}

// humanBytes renders a byte count as a size the owner can read in one glance.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}
