default: test

# Run the full test suite
test:
    go test ./...

test-v:
    go test -v ./...

# Static checks
lint:
    go vet ./...

# Build a local binary into bin/
build:
    go build -o bin/claudectx .

# Install into $GOBIN / $GOPATH/bin
install:
    go install .

# Health-check the local installation
doctor:
    go run . doctor
