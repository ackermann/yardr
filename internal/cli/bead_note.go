package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"yardr.dev/yardr/internal/store"
)

func (a *app) newBeadNoteCmd() *cobra.Command {
	var ackSeq uint64
	var noteFiles []string
	note := &cobra.Command{
		Use:   "note <id> <text> [--file <path>]... | note <id> --ack <seq>",
		Short: "Attach a note (evidence) to a bead",
		Long: `Attaches a note (evidence) to a bead: what was done, seen or decided, in
words. The bead's live session is told of it, unless the writer is that
session itself.

--file <path> attaches a file to the note, and may be given more than once
(up to ` + strconv.Itoa(store.MaxNoteFiles) + `). The yard keeps the file in its
home, in blobs/ under the SHA-256 of its bytes, once however often it is
attached, and where the note is read (yardr prime, yardr review, yardr bead
show) it stands under the note's words, on a line of its own starting with
"◆", as "1 image: <path>", "1 page: <path>" or "1 file: <path> (name)".
The note still needs its words: say what they show. What kind it is is read
from its bytes: an image is a PNG or a JPEG, a page is HTML, anything else
a file, which carries the path's base name; one over the yard's limit
(yardr settings get ` + store.BlobMaxSetting + `, 8 MiB unless set) is refused.

--ack <seq> writes nothing: it records that this session read note <seq>,
and takes no text and no --file.`,
		Args: func(c *cobra.Command, args []string) error {
			if c.Flags().Changed("ack") {
				if c.Flags().Changed("file") {
					return fmt.Errorf("note --ack records that this session read the note: it takes no --file")
				}
				if len(args) != 1 {
					return fmt.Errorf("note --ack records that this session read the note: it takes the bead and no text")
				}
				return nil
			}
			return cobra.ExactArgs(2)(c, args)
		},
		RunE: func(c *cobra.Command, args []string) error {
			st, err := a.store(c.Context())
			if err != nil {
				return err
			}
			// A bead@peer ref stands on no bead of this yard; it goes to
			// AddNote as typed, which refuses it with the way to mail
			// the asker.
			id := args[0]
			_, _, foreign := strings.Cut(id, "@")
			if !foreign {
				b, err := st.GetBead(c.Context(), id)
				if err != nil {
					return err
				}
				id = b.ID
			}
			if c.Flags().Changed("ack") {
				if ackSeq == 0 {
					return fmt.Errorf("--ack <seq>: a note's seq starts at 1")
				}
				n, err := st.AckNote(c.Context(), id, ackSeq, a.identity().session, a.who())
				if err != nil {
					return err
				}
				return a.out(map[string]any{"bead": id, "note": n.Seq, "acked": "ok"}, func() {
					fmt.Printf("note %d on %s acked\n", n.Seq, id)
				})
			}
			text := args[1]
			if text == "-" {
				if text, err = readAllStdin(); err != nil {
					return err
				}
			}
			if foreign && len(noteFiles) > 0 {
				// A note on another yard's question is always refused;
				// say so before any image is put.
				_, err := st.AddNotedAs(c.Context(), id, text, a.who(), a.identity().session)
				return err
			}
			if len(noteFiles) > 0 && strings.TrimSpace(text) == "" {
				return fmt.Errorf("a note with --file needs words: say what they show")
			}
			refs, err := putFiles(c.Context(), st, noteFiles)
			if err != nil {
				return err
			}
			n, err := st.AddNotedAs(c.Context(), id, text, a.who(), a.identity().session, blobFiles(refs)...)
			if err != nil {
				return err
			}
			out := map[string]any{"bead": id, "noted": "ok"}
			if n.Notify != "" {
				out["notify"] = n.Notify
			}
			if len(refs) > 0 {
				out["files"] = refs
			}
			return a.out(out, func() {
				said := fmt.Sprintf("note %d on %s", n.Seq, id)
				if n.Notify != "" {
					said += fmt.Sprintf(" (session %s will be told)", n.Notify)
				}
				fmt.Println(store.WithFiles(said, a.mailHome(), refs))
			})
		},
	}
	note.Flags().Uint64Var(&ackSeq, "ack", 0, "record that this session read note <seq>, instead of writing one")
	note.Flags().StringArrayVar(&noteFiles, "file", nil, "a file the note carries (repeatable)")
	return note
}
