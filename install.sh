#!/bin/bash

if ! mkdir xud
then
     echo "xud already installed"
else
    echo "starting xud clone..."
    if ! git clone https://github.com/ExchangeUnion/xud.git > /dev/null 2>&1; then
       echo "unable to git clone xud"
       exit 1
    fi
    echo "xud clone finished"

    echo "starting xud npm install..."
    cd xud
    if ! npm install > /dev/null 2>&1 ; then
        echo "unable to npm install xud"
        exit 1
    fi
    echo "finished xud npm install"

    echo "starting xud npm compile..."
    if ! npm run compile > /dev/null 2>&1 ; then
        echo "unable to build xud"
        exit 1
    fi
    echo "finished xud npm compile"
    cd ..
fi

if ! mkdir go
then
    echo "lnd already installed"
else
    export GOPATH=~/go/src/github.com/ExchangeUnion/xud-simulation/go

    echo "starting lnd clone..."
    if ! git clone -b resolver-cmd+simnet-ltcd https://github.com/ExchangeUnion/lnd.git  $GOPATH/src/github.com/lightningnetwork/lnd > /dev/null 2>&1; then
       echo "unable to git clone lnd"
       exit 1
    fi
    echo "finished lnd clone"

    echo "starting lnd make..."
    cd go/src/github.com/lightningnetwork/lnd
    if ! make > /dev/null 2>&1 ; then
        echo "unable to make lnd"
        exit 1
    fi
    echo "finished lnd make"

    cd ../../../../../
fi
