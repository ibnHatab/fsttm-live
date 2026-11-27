package live.fsttm.app;

import android.Manifest;
import android.app.Activity;
import android.content.pm.PackageManager;
import android.media.AudioFormat;
import android.media.AudioManager;
import android.media.AudioRecord;
import android.media.AudioTrack;
import android.media.MediaRecorder;
import android.os.Bundle;
import android.util.Log;
import android.view.WindowManager;
import android.webkit.WebView;
import android.webkit.WebViewClient;

import live.fsttm.mobile.Mobile;
import live.fsttm.mobile.Speaker;

/** The voice's page in a WebView; the mic goes to Go while the app is in front, Go's PCM to the speaker. */
public class MainActivity extends Activity implements Speaker {
    static final String TAG = "fsttm";
    static final int RATE = 16000;
    static final int VOICE_RATE = 24000;
    WebView web;
    volatile boolean listening;
    Thread mic;
    AudioTrack voice; // her speech, streamed

    @Override protected void onCreate(Bundle b) {
        super.onCreate(b);
        web = new WebView(this);
        web.getSettings().setJavaScriptEnabled(true);
        web.getSettings().setDomStorageEnabled(true);
        web.getSettings().setMediaPlaybackRequiresUserGesture(false);
        web.setWebViewClient(new WebViewClient());
        setContentView(web);
        getWindow().addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON // she is on screen while in front
                | WindowManager.LayoutParams.FLAG_SHOW_WHEN_LOCKED     // her tablet: over a swipe lock screen
                | WindowManager.LayoutParams.FLAG_DISMISS_KEYGUARD
                | WindowManager.LayoutParams.FLAG_TURN_SCREEN_ON);
        int vmin = AudioTrack.getMinBufferSize(VOICE_RATE, AudioFormat.CHANNEL_OUT_MONO, AudioFormat.ENCODING_PCM_16BIT);
        voice = new AudioTrack(AudioManager.STREAM_MUSIC, VOICE_RATE, AudioFormat.CHANNEL_OUT_MONO,
                AudioFormat.ENCODING_PCM_16BIT, Math.max(vmin, VOICE_RATE), AudioTrack.MODE_STREAM);
        voice.play();
        String url;
        try {
            url = Mobile.start(getFilesDir().getPath(), java.util.TimeZone.getDefault().getID(), this);
        } catch (Exception e) {
            Log.e(TAG, "start", e);
            web.loadData("<h3>Go side did not start: " + e + "</h3>", "text/html", "utf-8");
            return;
        }
        web.loadUrl(url);
    }

    @Override protected void onResume() {
        super.onResume();
        if (checkSelfPermission(Manifest.permission.RECORD_AUDIO) != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(new String[]{Manifest.permission.RECORD_AUDIO}, 1);
            return;
        }
        startMic();
    }

    @Override public void onRequestPermissionsResult(int code, String[] p, int[] r) {
        if (r.length > 0 && r[0] == PackageManager.PERMISSION_GRANTED) startMic();
    }

    @Override protected void onPause() {
        super.onPause();
        listening = false; // the mic is off whenever she is not in front
        Mobile.listen(false);
    }

    void startMic() {
        if (listening) return;
        listening = true;
        Mobile.listen(true);
        mic = new Thread(new Runnable() { public void run() {
            int min = AudioRecord.getMinBufferSize(RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT);
            // VOICE_COMMUNICATION: the platform's echo cancelling and noise suppression
            AudioRecord ar = new AudioRecord(MediaRecorder.AudioSource.VOICE_COMMUNICATION, RATE,
                    AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT, Math.max(min, RATE));
            byte[] buf = new byte[RATE / 50 * 2]; // 20 ms
            ar.startRecording();
            while (listening) {
                int n = ar.read(buf, 0, buf.length);
                if (n > 0) Mobile.mic(n == buf.length ? buf : java.util.Arrays.copyOf(buf, n));
            }
            ar.stop();
            ar.release();
        }}, "mic");
        mic.start();
    }

    /** Speaker: Go's PCM (s16le mono, 16 kHz) to an AudioTrack. */
    @Override public void play(byte[] pcm) {
        AudioTrack t = new AudioTrack(AudioManager.STREAM_MUSIC, RATE, AudioFormat.CHANNEL_OUT_MONO,
                AudioFormat.ENCODING_PCM_16BIT, Math.max(pcm.length, 4096), AudioTrack.MODE_STATIC);
        t.write(pcm, 0, pcm.length);
        t.play();
        try { Thread.sleep(pcm.length * 1000L / (RATE * 2) + 100); } catch (InterruptedException ignored) {}
        t.release();
    }

    /** Speaker: her voice as it streams (s16le mono, 24 kHz); Go paces it in real time. */
    @Override public void voice(byte[] pcm) {
        voice.write(pcm, 0, pcm.length);
    }

    /** Speaker: she was interrupted: what the track holds is dropped. */
    @Override public void cut() {
        voice.pause();
        voice.flush();
        voice.play();
    }

    @Override protected void onDestroy() {
        listening = false;
        Mobile.stop();
        voice.release();
        super.onDestroy();
    }
}
