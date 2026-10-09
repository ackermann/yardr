package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"slices"

	bolt "go.etcd.io/bbolt"
)

// A file crosses a link (docs/peers.md) as a message of kind PeerFile: the
// bytes of one blob (blobs.go), with the name they are to have. Mail with
// images to another yard is those messages and then the mail that names
// them, in that order in the outbox, which never reorders: when the mail is
// taken its blobs are there. The yard that takes a file checks its bytes
// against the hash claimed and keeps it as it keeps a blob put here.
//
// file is a kind of its own on a link, apart from mail: whether a peer's
// bytes may land on this yard's disk is the operator's to say, whoever is
// trusted to write (Peer.Trust). Mail whose file was not taken is still
// delivered, and says so where the file's path would be
// (BlobRef.NotTaken): nothing waits for a file that did not come.
const PeerFile = "file"

const (
	// PeerFileMaxSetting is the most bytes one file sent to a peer, or
	// taken from one, may have. Unset, it is what a blob of the yard may
	// have (BlobMaxSetting).
	PeerFileMaxSetting = "peer_file_max_bytes"
	// MaxPeerFile is the most the setting can allow: a file is one message,
	// read whole by the yard that takes it before it can say no, so every
	// yard reads a message as large as this from a peer and none larger.
	MaxPeerFile = 32 << 20
	// MaxPeerFileBody bounds the body of a message of kind PeerFile: a
	// file of MaxPeerFile in base64, and room for its name.
	MaxPeerFileBody = (MaxPeerFile+2)/3*4 + 1<<10
)

// RefusedNotFile is a message of kind PeerFile that is no file: a body that
// does not read, bytes that are not what the hash claims, or a type no
// blob has.
const RefusedNotFile = "not_file"

// Why a file of a peer's mail was not taken, as its line says (FilesLine).
const (
	notTakenLink = "the link does not take files"
	// Of one that passed what is asked at the mail: its message was refused
	// for what it is, was given up by the sender, or never came.
	notTakenAbsent = "it did not arrive"
)

// peerFile is the body of a message of kind PeerFile. Data is the bytes,
// in base64 where it is JSON; hash, type and bytes are the sender's word
// for them, and only the hash is held to (takeFile).
type peerFile struct {
	BlobRef
	Data []byte `json:"data,omitempty"`
}

// maxPeerBody bounds the body of a message of kind.
func maxPeerBody(kind string) int {
	if kind == PeerFile {
		return MaxPeerFileBody
	}
	return MaxPeerBody
}

// peerFileMax is the yard's cap on one file to or from a peer.
func peerFileMax(tx *bolt.Tx) (int64, error) {
	limit, err := bytesSetting(tx, BlobMaxSetting, DefaultBlobMax)
	if err != nil {
		return 0, err
	}
	limit, err = bytesSetting(tx, PeerFileMaxSetting, limit)
	return min(limit, MaxPeerFile), err
}

// SendsFiles says why mail to the peer can carry no file, nil when it can:
// the link carries file out, and the peer's yardr said it understands the
// kind. One that has not said is an older yardr, which would take the mail
// and pass its files by.
func (p *Peer) SendsFiles() error {
	switch {
	case !p.carries(PeerFile, false):
		return fmt.Errorf("peer %s: the link does not carry %s out, so mail to it carries no image: the operator allows it with yardr peer allow %s --send %s", p.Name, PeerFile, p.Name, PeerFile)
	case p.Said == nil:
		return fmt.Errorf("peer %s has not said what its yardr understands (an older yardr, or nothing has come from it yet), so mail to it carries no image", p.Name)
	case !p.Said.Knows(PeerFile):
		return fmt.Errorf("peer %s: its yardr (%s) does not understand %s, so mail to it carries no image until it is upgraded", p.Name, or(p.Said.Yardr, "a build it does not name"), PeerFile)
	}
	return nil
}

// peerFiles are the blobs hashes name, as mail to the peer name carries
// them, and the messages that take their bytes there, one for each blob.
// It is read before the write that queues them: a blob is megabytes. What
// the link or this yard's cap does not let out refuses the mail, here,
// before anything is queued.
func (s *Store) peerFiles(name string, hashes []string) ([]BlobRef, []PeerMessage, error) {
	if len(hashes) == 0 {
		return nil, nil, nil
	}
	refs, err := s.mailFiles(hashes)
	if err != nil {
		return nil, nil, err
	}
	var limit int64
	err = s.read(func(tx *bolt.Tx) error {
		p, err := loadPeer(tx, name)
		if err != nil {
			return err
		}
		if err := p.SendsFiles(); err != nil {
			return err
		}
		limit, err = peerFileMax(tx)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	var msgs []PeerMessage
	for i, ref := range refs {
		if ref.Bytes > limit {
			return nil, nil, fmt.Errorf("the image %s is %d bytes, over what this yard sends another of %d (yardr settings set %s <bytes>, at most %d)",
				ref.Hash, ref.Bytes, limit, PeerFileMaxSetting, MaxPeerFile)
		}
		// Named twice, it crosses once.
		if slices.ContainsFunc(refs[:i], func(r BlobRef) bool { return r.Hash == ref.Hash }) {
			continue
		}
		data, err := os.ReadFile(s.BlobPath(ref.Hash))
		if err != nil {
			return nil, nil, err
		}
		body, err := json.Marshal(peerFile{BlobRef: ref, Data: data})
		if err != nil {
			return nil, nil, err
		}
		m := PeerMessage{ID: newPeerMessageID(), Kind: PeerFile, Body: body}
		if err := m.check(); err != nil {
			return nil, nil, err
		}
		msgs = append(msgs, m)
	}
	return refs, msgs, nil
}

// withoutData is the body of a file message with its bytes left out: what
// is kept of one that was given up, which stays in the outbox until it is
// pruned.
func withoutData(body json.RawMessage) json.RawMessage {
	var f peerFile
	if json.Unmarshal(body, &f) != nil {
		return json.RawMessage(`{}`)
	}
	f.Data = nil
	out, err := json.Marshal(f)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return out
}

// carrier is the mail the file message f crosses for, as it waits in the
// outbox, and the hash it names the file by: nil when the mail is not
// there. A mail's files are queued right before it in one write
// (MailPeer), and no message overtakes one before it, so it is the first
// mail behind f, and waits there for as long as f does.
func (w *wtx) carrier(f *PeerOut) (*PeerOut, string) {
	var file peerFile
	if json.Unmarshal(f.Body, &file) != nil {
		return nil, ""
	}
	c := w.Bucket(bPeerOut).Cursor()
	prefix := []byte(join(f.Peer, ""))
	for k, raw := c.Seek([]byte(join(f.Peer, seqKey(f.Seq+1)))); k != nil && bytes.HasPrefix(k, prefix); k, raw = c.Next() {
		var m PeerOut
		if json.Unmarshal(raw, &m) != nil || m.Kind == PeerFile {
			continue
		}
		var mail peerMail
		if m.Kind != PeerMail || json.Unmarshal(m.Body, &mail) != nil || !slices.ContainsFunc(mail.Files, func(r BlobRef) bool { return r.Hash == file.Hash }) {
			break
		}
		return &m, file.Hash
	}
	return nil, ""
}

// filing is what taking the files of a batch of messages needs done before
// the write, as taking is for its beads: megabytes are not hashed and
// written to disk inside one.
type filing struct {
	// limit is the yard's cap on a file from a peer.
	limit int64
	// refused says of each file message that was looked at, by its id, why
	// it is not taken: "" for one whose blob the yard now holds. One that
	// is not in it was not looked at, because the link took no file then.
	refused map[string]string
	// extra is what the refusal's event says besides, by the same id.
	extra map[string]map[string]any
	// held are the blobs the yard holds of those the batch's mail names,
	// by hash, as the yard has them.
	held map[string]BlobRef
	// err is what kept a file from being stored that is no fault of the
	// file's: the batch is not taken, and the sender tries again.
	err error
}

// filing takes the files among msgs from the peer name: each that the link
// lets in and that is what it claims to be is kept as a blob, now, so that
// the write only has to say so. A blob kept for a write that then fails is
// one no letter names, and goes with a prune. It also reads which blobs
// the mail among msgs names are held, after the files of the same batch
// were kept.
//
// What would be over the peer's files of the hour (PeerFileLimit) is
// refused here, unread: the count is the write's to keep (checkFile), and
// bytes kept first would be on disk whatever it then said. For the same
// reason a message taken before is passed by, which the write drops by its
// id whatever its bytes are now.
func (s *Store) filing(name string, msgs []PeerMessage) *filing {
	fl := &filing{limit: DefaultBlobMax, refused: map[string]string{}, extra: map[string]map[string]any{}, held: map[string]BlobRef{}}
	var p *Peer
	seen := map[string]bool{}
	fl.err = s.read(func(tx *bolt.Tx) (err error) {
		if p, err = loadPeer(tx, name); err != nil {
			// The write says that there is no such peer.
			p = nil
			return nil
		}
		for _, m := range msgs {
			if m.Kind == PeerFile && tx.Bucket(bPeerSeen).Get([]byte(join(name, m.ID))) != nil {
				seen[m.ID] = true
			}
		}
		fl.limit, err = peerFileMax(tx)
		return err
	})
	if fl.err != nil || p == nil || !p.carries(PeerFile, true) {
		return fl
	}
	// p is this read's own: nothing of it is saved.
	p.hour(now())
	taken := map[string]bool{}
	for _, m := range msgs {
		if m.check() != nil {
			continue
		}
		switch m.Kind {
		case PeerFile:
			if _, twice := fl.refused[m.ID]; twice || seen[m.ID] || taken[m.ID] {
				continue
			}
			if p.Files++; p.Files > PeerFileLimit {
				fl.refused[m.ID] = RefusedRate
				continue
			}
			reason, extra, err := s.takeFile(m, fl.limit)
			if err != nil {
				fl.err = err
				return fl
			}
			fl.refused[m.ID], fl.extra[m.ID] = reason, extra
			if reason == "" {
				taken[m.ID] = true
			}
		case PeerMail:
			var mail peerMail
			if json.Unmarshal(m.Body, &mail) != nil || len(mail.Files) > MaxMailFiles {
				continue
			}
			for _, f := range mail.Files {
				if ref, err := s.Blob(f.Hash); err == nil {
					fl.held[f.Hash] = *ref
				}
			}
		}
	}
	return fl
}

// takeFile keeps the file the message m carries as a blob of this yard, or
// says why not, with what the refusal's event says besides. The hash is
// what the mail will name it by, so bytes that are not the hash's are
// refused; the type is what the bytes say, as for a blob put here, and the
// size is held to the yard's cap for a peer's file. Bytes the yard holds
// already are not written again.
func (s *Store) takeFile(m PeerMessage, limit int64) (reason string, extra map[string]any, err error) {
	var f peerFile
	if json.Unmarshal(m.Body, &f) != nil || !blobHash.MatchString(f.Hash) || len(f.Data) == 0 {
		return RefusedNotFile, map[string]any{"detail": "the body does not read as a file"}, nil
	}
	extra = map[string]any{"hash": f.Hash, "bytes": len(f.Data)}
	if int64(len(f.Data)) > limit {
		extra["limit"] = limit
		return RefusedTooLarge, extra, nil
	}
	sum := sha256.Sum256(f.Data)
	typ, ok := blobType(f.Data)
	switch {
	case hex.EncodeToString(sum[:]) != f.Hash:
		extra["detail"] = "the bytes are not those of the hash"
		return RefusedNotFile, extra, nil
	case !ok:
		extra["detail"] = "the bytes are " + typ
		return RefusedNotFile, extra, nil
	}
	s.blobMu.Lock()
	defer s.blobMu.Unlock()
	_, err = s.keepBlob(&BlobRef{Hash: f.Hash, Type: typ, Bytes: int64(len(f.Data))}, f.Data)
	return "", nil, err
}

// checkFile says whether the file message m from p is taken, which was
// decided before the write (filing): "" when its blob is held, or why not,
// logged as peer_message_refused.
//
// Every file that comes counts toward the peer's hour, as its mail does
// (checkMail), the refused too, and one over the limit is refused whatever
// was decided of it; of those only the first of the hour is logged.
func (w *wtx) checkFile(p *Peer, m PeerMessage, way string, fl *filing) (string, error) {
	p.hour(now())
	p.Files++
	reason, looked := fl.refused[m.ID]
	extra := map[string]any{"direction": "in", "way": way}
	switch {
	case p.Files > PeerFileLimit || reason == RefusedRate:
		reason = RefusedRate
		if p.FileLimited {
			return reason, nil
		}
		p.FileLimited = true
		extra["limit"], extra["until"] = PeerFileLimit, p.MailSince.Add(PeerMailWindow)
	case !looked:
		// Allowed between the look and the write: the file was not read,
		// and what the link said then is the answer.
		reason = RefusedKind
	case reason == "":
		return "", nil
	}
	for k, v := range fl.extra[m.ID] {
		extra[k] = v
	}
	extra["reason"] = reason
	return reason, w.event("", "peer_message_refused", "peer:"+p.Name, peerEvent(p.Name, m, extra))
}

// mailFiles are the files of the mail from p as its event carries them:
// each as the yard holds it, or with why it does not (BlobRef.NotTaken).
// The link and the cap are asked here again, of what the mail claims, so a
// file that was refused for either says which; any other that is not held
// did not arrive, whatever became of its message.
func (fl *filing) mailFiles(p *Peer, mail *peerMail) []BlobRef {
	var out []BlobRef
	for _, f := range mail.Files {
		held, ok := fl.held[f.Hash]
		ref := BlobRef{Hash: f.Hash, Bytes: f.Bytes}
		if slices.Contains(BlobTypes, f.Type) {
			ref.Type = f.Type
		}
		switch {
		case !p.carries(PeerFile, true):
			ref.NotTaken = notTakenLink
		case ok:
			// A file the yard took was checked against its actual size and
			// type. The mail's copies of those fields are only claims; they
			// must not turn a held blob into a missing one.
			ref = held
		case f.Bytes > fl.limit:
			ref.NotTaken = fmt.Sprintf("it is %d bytes, over this yard's limit of %d for a file from another yard", f.Bytes, fl.limit)
		default:
			ref.NotTaken = notTakenAbsent
		}
		out = append(out, ref)
	}
	return out
}
