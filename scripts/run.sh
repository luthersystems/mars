#!/bin/bash
export USER_ID="${USER_ID:-0}"
export GROUP_ID="${GROUP_ID:-0}"

if [ x"$USER_ID" != x"0" ]; then
    echo "default:x:${USER_ID}:${GROUP_ID}:Default User:${HOME}:/usr/sbin/nologin" >> /etc/passwd
    chown -R $USER_ID:$GROUP_ID $HOME
    user=default
else
    user=root
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
su -s "/bin/bash" -c "${CMD}" $user
