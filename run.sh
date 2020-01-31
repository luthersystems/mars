#!/bin/bash

if [ x"$USER_ID" != x"0" ]; then
	echo "default:x:${USER_ID}:${GROUP_ID}:Default User:${HOME}:/usr/sbin/nologin" >> /etc/passwd
fi
pwconv
grpconv
q() {
    echo -n "'"
    echo -n "$1" | sed "s/'/'\"'\"'/"
    echo -n "'"
}
quote_args() {
    for arg in "$@"; do
        q "$arg"
        echo -n " "
    done
}
CMD="PATH=$PATH /opt/mars/mars.py $(quote_args "$@")"
su -s "/bin/bash" -c "${CMD}" default
