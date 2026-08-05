// release-manifest creates the one signed component inventory consumed by
// bootstrap and update. It is a release-workflow tool, not part of the product.
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/lev-goryachev/lctk/internal/inference"
	"github.com/lev-goryachev/lctk/internal/releasebundle"
)

type artifactFlags []string

func (values *artifactFlags) String() string { return strings.Join(*values, ";") }
func (values *artifactFlags) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func main() {
	var artifacts artifactFlags
	version := flag.String("version", "", "release version")
	commit := flag.String("commit", "", "release commit")
	published := flag.String("published-at", "", "RFC3339 publication time")
	baseURL := flag.String("base-url", "", "release download base URL")
	codeImage := flag.String("code-image", "", "immutable code-intel image reference")
	codeBytes := flag.Int64("code-image-bytes", 0, "compressed code-intel bytes")
	inferenceBytes := flag.Int64("inference-image-bytes", 0, "compressed inference image bytes")
	keyID := flag.String("key-id", "", "release signing key identifier")
	output := flag.String("output", "", "signed envelope output path")
	printPublic := flag.Bool("print-public-key", false, "print the release public key and exit")
	flag.Var(&artifacts, "artifact", "name,kind,os,arch,path; repeat for every artifact")
	flag.Parse()

	private, err := privateKeyFromEnvironment()
	if err != nil {
		fatal(err)
	}
	if *printPublic {
		fmt.Print(base64.StdEncoding.EncodeToString(private.Public().(ed25519.PublicKey)))
		return
	}
	if *version == "" || *commit == "" || *published == "" || *baseURL == "" ||
		*codeImage == "" || *codeBytes <= 0 || *inferenceBytes <= 0 || *keyID == "" || *output == "" {
		fatal(errors.New("complete release identity, image sizes, key id, output, and artifacts are required"))
	}
	if _, err := time.Parse(time.RFC3339, *published); err != nil {
		fatal(fmt.Errorf("published-at: %w", err))
	}
	parsedArtifacts, err := loadArtifacts(artifacts, strings.TrimRight(*baseURL, "/"))
	if err != nil {
		fatal(err)
	}
	codeDigest, err := imageDigest(*codeImage)
	if err != nil {
		fatal(err)
	}
	inferenceDigest, err := imageDigest(inference.Image)
	if err != nil {
		fatal(err)
	}
	manifest := releasebundle.Manifest{
		SchemaVersion:      releasebundle.SchemaVersion,
		Version:            *version,
		Commit:             *commit,
		PublishedAt:        *published,
		MinimumHostVersion: "0.1.0",
		ProjectSchemaFrom:  1,
		ProjectSchemaTo:    2,
		Artifacts:          parsedArtifacts,
		CodeImage: releasebundle.Image{
			Name: "code-intel", Reference: *codeImage, Digest: codeDigest,
			CompressedBytes: *codeBytes, Platforms: []string{"linux/amd64", "linux/arm64"},
		},
		InferenceImage: releasebundle.Image{
			Name: "embedding-inference", Reference: inference.Image, Digest: inferenceDigest,
			CompressedBytes: *inferenceBytes, Platforms: []string{"linux/amd64", "linux/arm64"},
		},
		EmbeddingModel: releasebundle.Model{
			Name: inference.ModelName, URL: inference.ModelURL, Bytes: inference.ModelBytes,
			SHA256: inference.ModelSHA256, License: "Apache-2.0",
		},
		MigrationNotesURL:    strings.TrimRight(*baseURL, "/") + "/migration-notes.md",
		RollbackInstructions: "Run `lctk update rollback`; see migration-notes.md for state and manual recovery evidence.",
	}
	if err := manifest.Validate(); err != nil {
		fatal(err)
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		fatal(err)
	}
	envelope := releasebundle.Envelope{
		KeyID: *keyID, Algorithm: "ed25519",
		Payload:   base64.StdEncoding.EncodeToString(payload),
		Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(private, payload)),
	}
	encoded, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		fatal(err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(*output, encoded, 0o600); err != nil {
		fatal(err)
	}
}

func privateKeyFromEnvironment() (ed25519.PrivateKey, error) {
	encoded := os.Getenv("LCTK_RELEASE_ED25519_PRIVATE_KEY")
	if encoded == "" {
		return nil, errors.New("LCTK_RELEASE_ED25519_PRIVATE_KEY is required")
	}
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("release private key is not valid base64")
	}
	switch len(decoded) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(decoded), nil
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(decoded), nil
	default:
		return nil, fmt.Errorf("release private key has %d bytes, want %d or %d", len(decoded), ed25519.SeedSize, ed25519.PrivateKeySize)
	}
}

func loadArtifacts(specifications []string, baseURL string) ([]releasebundle.Artifact, error) {
	artifacts := make([]releasebundle.Artifact, 0, len(specifications))
	for _, specification := range specifications {
		fields := strings.Split(specification, ",")
		if len(fields) != 5 {
			return nil, fmt.Errorf("artifact %q must have name,kind,os,arch,path", specification)
		}
		name, kind, targetOS, arch, path := fields[0], fields[1], fields[2], fields[3], fields[4]
		size, digest, err := fileIdentity(path)
		if err != nil {
			return nil, fmt.Errorf("artifact %s: %w", name, err)
		}
		artifacts = append(artifacts, releasebundle.Artifact{
			Name: name, Kind: kind, OS: targetOS, Arch: arch,
			URL: baseURL + "/" + name, Bytes: size, SHA256: digest,
		})
	}
	return artifacts, nil
}

func fileIdentity(path string) (int64, string, error) {
	file, err := os.Open(filepath.Clean(path))
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return 0, "", err
	}
	return size, hex.EncodeToString(hash.Sum(nil)), nil
}

func imageDigest(reference string) (string, error) {
	_, digest, found := strings.Cut(reference, "@sha256:")
	if !found || len(digest) != 64 {
		return "", fmt.Errorf("image %q has no sha256 digest", reference)
	}
	if _, err := strconv.ParseUint(digest[:16], 16, 64); err != nil {
		return "", fmt.Errorf("image %q has an invalid digest", reference)
	}
	return "sha256:" + digest, nil
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "release-manifest:", err)
	os.Exit(1)
}
