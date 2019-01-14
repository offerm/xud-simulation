#!/bin/bash
make_dir () {
if ! mkdir "$1"; then
	echo "$1 already exists"
	exit 0
fi
}

make_dir xud

if ! git clone https://github.com/ExchangeUnion/xud.git > /dev/null 2>&1; then
	echo "unable to git clone xud"
	exit 1
fi
cd xud
if ! npm install > /dev/null 2>&1 ; then
	echo "unable to npm install xud"
	exit 1
fi
if ! npm run compile > /dev/null 2>&1 ; then
	echo "unable to build xud"
	exit 1
fi
echo "xud installed"



