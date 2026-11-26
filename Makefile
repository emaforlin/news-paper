GO        ?= go
COVER_MIN ?= 90

.PHONY: check fmt vet test cover race bench

check: fmt vet test

fmt:
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

# Fails if any package under internal/ is below COVER_MIN% statement coverage.
# A package without tests reports 0.0% and fails too.
cover:
	@$(GO) test -cover ./internal/... | tee /dev/stderr | awk -v min=$(COVER_MIN) ' \
	  { for (i = 1; i <= NF; i++) if ($$i == "coverage:") { p = $$(i+1); sub("%", "", p); \
	      if (p + 0 < min) { print "coverage below " min "%: " $$0; bad = 1 } } } \
	  /^(FAIL|---)/ { bad = 1 } \
	  END { exit bad }'

# Full pipeline offline against tools/fixtureserver, writing dist/bench.epub.
# PAGES is a directory of <name>/source.html files.
bench:
	@GO="$(GO)" PAGES="$(PAGES)" scripts/bench.sh
