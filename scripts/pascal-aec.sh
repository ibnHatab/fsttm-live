#!/bin/sh
# Software echo cancellation on a desktop (PulseAudio's WebRTC canceller) for testing barge-in
# while Nina plays from the PC's own speakers, which the ReSpeaker has no reference for.
#   up:   ReSpeaker ch0 → nina_ec_source;  Nina plays to nina_ec_sink → the PC's speakers
#   down: unload, and give the ReSpeaker back to ALSA (arecord -D hw:ArrayUAC10)
# nina-live -dev pulse:nina_ec_source -channels 1 -ref -1 -out pulse:nina_ec_sink -person-sink "$SINK"
set -e
CARD=$(pactl list cards short | awk '/ReSpeaker/{print $2}')
SINK=${SINK:-$(pactl get-default-sink)}
case "$1" in
up)
	pactl set-card-profile "$CARD" input:multichannel-input
	sleep 1
	SRC=$(pactl list short sources | awk '/SEEED/ && !/monitor/{print $2}')
	pactl load-module module-remap-source source_name=rs_ch0 master="$SRC" channels=1 \
		master_channel_map=front-left channel_map=mono remix=no >/dev/null
	pactl load-module module-echo-cancel source_master=rs_ch0 sink_master="$SINK" \
		source_name=nina_ec_source sink_name=nina_ec_sink aec_method=webrtc use_master_format=1 \
		'aec_args="analog_gain_control=0 digital_gain_control=0"' >/dev/null
	echo "nina_ec_source / nina_ec_sink over $SINK" ;;
down)
	for m in $(pactl list modules short | awk '/module-echo-cancel|source_name=rs_ch0/{print $1}'); do
		pactl unload-module "$m" 2>/dev/null || true # unloading the canceller may take the remap with it
	done
	pactl set-card-profile "$CARD" off ;;
*) echo "usage: $0 up|down" >&2; exit 2 ;;
esac
