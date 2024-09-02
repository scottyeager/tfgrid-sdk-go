#!/bin/sh

set -ex 

if [ -z ${VERSION+x} ]
then
    echo 'Error! $VERSION is required.'
    exit 64
fi

echo $VERSION

goreleaser check
goreleaser release --snapshot --clean

tag_and_push() {
    local component="$1"
    git tag -a "$component/$VERSION" -m "release $component/$VERSION"
    git push origin $component/$VERSION
}


tag_and_push "grid-client"
tag_and_push "grid-proxy"

# # main
git tag -a $VERSION -m "release $VERSION"
git push origin $VERSION
