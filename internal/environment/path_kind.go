package environment

import "os"

type candidateKind uint8

const (
	candidateMissing candidateKind = iota
	candidateRegular
	candidateDirectory
	candidateUnsupported
)

func candidateKindForInfo(info os.FileInfo) candidateKind {
	if info == nil {
		return candidateMissing
	}
	if info.Mode().IsRegular() {
		return candidateRegular
	}
	if info.IsDir() {
		return candidateDirectory
	}
	return candidateUnsupported
}
