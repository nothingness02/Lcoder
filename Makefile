.PHONY: build-desktop
build-desktop:
	cd cmd/lcoder-desktop && wails build

.PHONY: test-desktop
test-desktop:
	cd cmd/lcoder-desktop && go test ./... -count=1
	cd cmd/lcoder-desktop/frontend && npm test
