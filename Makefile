# deadalyze: the analysis bundle, the algorithms, the corpus and the harnesses.

GRIDS ?= $(HOME)/grids

.PHONY: test bundle bundle-inspect corpus-stats grideval grideval-fresh pins

test:
	go vet ./... && go test ./...
	bash -n bundle/build.sh

# The runners here, deadcatalog's embedded copies and DEADCA7's Swift literals
# all hash the same; this is the check.
pins:
	go test ./algos/ -run 'Pinned|Siblings|Swift' -v

bundle:
	./bundle/build.sh

bundle-inspect:
	go run ./cmd/bundle inspect bundle/dist/deadca7-ml-$$(go env GOOS)-$$(go env GOARCH).tar.gz

corpus-stats:
	go run ./cmd/corpus stats $(GRIDS)

# Stored deadca7 grids against rekordbox's, no runtime, seconds.
grideval:
	go run ./cmd/grideval -corpus-dir $(GRIDS) -json build/grideval.json

# The current engine on the audio, about twenty seconds a track. AUDIO is
# where the files are; the runtime is dc's installed one or DEADCATALOG_RUNTIME.
grideval-fresh:
	go run ./cmd/grideval -corpus-dir $(GRIDS) -fresh -audio $(AUDIO) -json build/grideval-fresh.json
