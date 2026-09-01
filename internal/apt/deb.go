package apt

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/MinaProtocol/mina-provision/internal/extract"
)

// arMagic opens every ar archive, which is the container format of a .deb.
const arMagic = "!<arch>\n"

// arHeaderSize is the fixed size of an ar member header. The layout is
// name[16] mtime[12] uid[6] gid[6] mode[8] size[10] magic[2].
const arHeaderSize = 60

// ExtractData unpacks the data member of a .deb into dstDir and returns the
// files written.
//
// Only a gzip-compressed data member is supported, because the Mina packages
// are built with `dpkg-deb -Zgzip`: the build forces gzip so that packages
// produced on a newer Ubuntu stay installable on older distributions. A
// package compressed any other way is therefore not something to work around
// silently, and is reported as such.
func ExtractData(debPath, dstDir string) ([]string, error) {
	f, err := os.Open(debPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	magic := make([]byte, len(arMagic))
	if _, err := io.ReadFull(f, magic); err != nil {
		return nil, fmt.Errorf("%s: read ar magic: %w", debPath, err)
	}
	if string(magic) != arMagic {
		return nil, fmt.Errorf("%s is not a .deb (bad ar magic)", debPath)
	}

	var seen []string
	for {
		name, size, err := readARHeader(f)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", debPath, err)
		}
		seen = append(seen, name)

		if strings.HasPrefix(name, "data.tar") {
			if name != "data.tar.gz" {
				return nil, fmt.Errorf("%s: data member is %q, but only gzip is expected "+
					"(Mina packages are built with dpkg-deb -Zgzip)", debPath, name)
			}
			return extract.TarGzReader(io.LimitReader(f, size), dstDir)
		}

		// Skip this member, including the padding byte that keeps members
		// aligned to an even offset.
		skip := size
		if size%2 == 1 {
			skip++
		}
		if _, err := io.CopyN(io.Discard, f, skip); err != nil {
			return nil, fmt.Errorf("%s: skip member %s: %w", debPath, name, err)
		}
	}
	return nil, fmt.Errorf("%s: no data.tar.* member (members: %s)", debPath, strings.Join(seen, ", "))
}

// readARHeader reads one ar member header and returns the member's name and
// size. Names are padded with spaces and, in the GNU variant dpkg emits, may
// carry a trailing slash.
func readARHeader(r io.Reader) (name string, size int64, err error) {
	buf := make([]byte, arHeaderSize)
	if _, err := io.ReadFull(r, buf); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return "", 0, io.EOF
		}
		return "", 0, err
	}
	if string(buf[58:60]) != "`\n" {
		return "", 0, fmt.Errorf("malformed ar member header")
	}
	name = strings.TrimSuffix(strings.TrimSpace(string(buf[0:16])), "/")
	size, err = strconv.ParseInt(strings.TrimSpace(string(buf[48:58])), 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("ar member %q: bad size: %w", name, err)
	}
	return name, size, nil
}
