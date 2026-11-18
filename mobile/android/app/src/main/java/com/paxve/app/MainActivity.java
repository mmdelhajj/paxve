package com.paxve.app;

import android.graphics.Color;
import android.os.Bundle;
import android.webkit.CookieManager;
import android.webkit.WebView;
import android.widget.Toast;

import androidx.activity.OnBackPressedCallback;

import com.getcapacitor.BridgeActivity;

public class MainActivity extends BridgeActivity {
    private static final int BRAND = Color.parseColor("#0F766E");
    private long lastBackAt = 0;

    @Override
    public void onCreate(Bundle savedInstanceState) {
        registerPlugin(PaxvePrinterPlugin.class); // classic Bluetooth receipt printers
        registerPlugin(PaxveAppPlugin.class);     // print, save/share files, external links, app info
        super.onCreate(savedInstanceState);

        // the status/navigation bar areas (Capacitor pads the WebView by the system bar insets) show this colour
        getWindow().getDecorView().setBackgroundColor(BRAND);

        CookieManager.getInstance().setAcceptCookie(true);

        // Back button: let the web app close a dialog or go back first; on the first screen press twice to exit.
        getOnBackPressedDispatcher().addCallback(this, new OnBackPressedCallback(true) {
            @Override
            public void handleOnBackPressed() {
                WebView wv = bridge == null ? null : bridge.getWebView();
                if (wv == null) {
                    finish();
                    return;
                }
                wv.evaluateJavascript(
                    "(function(){try{return window.__paxveBack?window.__paxveBack():'none'}catch(e){return 'none'}})()",
                    result -> {
                        String r = result == null ? "" : result.replace("\"", "");
                        if (r.equals("handled")) return;
                        if (r.equals("none") && wv.canGoBack()) {
                            wv.goBack();
                            return;
                        }
                        long now = System.currentTimeMillis();
                        if (now - lastBackAt < 2000) {
                            finish();
                        } else {
                            lastBackAt = now;
                            Toast.makeText(MainActivity.this, exitHint(), Toast.LENGTH_SHORT).show();
                        }
                    });
            }
        });
    }

    private String exitHint() {
        String lang = getResources().getConfiguration().getLocales().get(0).getLanguage();
        if ("ar".equals(lang)) return "اضغط رجوع مرة أخرى للخروج";
        if ("fr".equals(lang)) return "Appuyez encore sur Retour pour quitter";
        return "Press back again to exit";
    }

    @Override
    public void onPause() {
        super.onPause();
        CookieManager.getInstance().flush(); // keep the sign-in when Android kills the app
    }
}
