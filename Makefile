VIZ_NODE_MODULES := "filecoin-network-viz/viz-circle/node_modules"
EXPLORER_NODE_MODULES := "filecoin-explorer/node_modules"

build:
	cd filnetsim && go build

install:
	cd filnetsim && go install

test:
	go test ./...

run: build
	filnetsim/filnetsim

runDebug: build deps frontend
	filnetsim/filnetsim --debug

deps: submodules bin/go-filecoin

frontend: submodules viz explorer
.PHONY: frontend

viz: $(VIZ_NODE_MODULES)
	(cd filecoin-network-viz/viz-circle; npm run build)

$(VIZ_NODE_MODULES):
	(cd filecoin-network-viz/viz-circle; npm install)

explorer: $(EXPLORER_NODE_MODULES)
	(cd filecoin-explorer; EXPLORER_BASEPATH="/explorer/" npm run build)

$(EXPLORER_NODE_MODULES):
	(cd filecoin-explorer; npm install)

bin/go-filecoin:
	@bin/build-filecoin.sh

submodules:
	git submodule init
	git submodule update
