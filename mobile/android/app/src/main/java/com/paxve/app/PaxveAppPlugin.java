package com.paxve.app;

import android.content.ActivityNotFoundException;
import android.content.ContentResolver;
import android.content.ContentValues;
import android.content.Context;
import android.content.Intent;
import android.content.pm.PackageInfo;
import android.net.Uri;
import android.os.Build;
import android.os.Environment;
import android.print.PrintAttributes;
import android.print.PrintDocumentAdapter;
import android.print.PrintManager;
import android.provider.MediaStore;
import android.util.Base64;
import android.webkit.WebView;
import android.widget.Toast;

import androidx.core.content.FileProvider;

import com.getcapacitor.JSObject;
import com.getcapacitor.Plugin;
import com.getcapacitor.PluginCall;
import com.getcapacitor.PluginMethod;
import com.getcapacitor.annotation.CapacitorPlugin;

import java.io.File;
import java.io.FileOutputStream;
import java.io.OutputStream;
import java.net.URISyntaxException;

/**
 * Things an Android WebView cannot do on its own:
 *  - print()       window.print() is a no-op in WebView → Android print dialog (printer or "Save as PDF")
 *  - saveFile()    blob/data downloads are ignored → write to Downloads and/or open the share sheet
 *  - openExternal  WhatsApp / tel: / mailto: / sms: / other sites → the right app
 *  - getAppInfo()  installed versionCode / versionName (update notice)
 *  - exitApp()     back button on the first screen
 */
@CapacitorPlugin(name = "PaxveApp")
public class PaxveAppPlugin extends Plugin {

    @PluginMethod
    public void getAppInfo(PluginCall call) {
        JSObject r = new JSObject();
        try {
            Context c = getContext();
            PackageInfo p = c.getPackageManager().getPackageInfo(c.getPackageName(), 0);
            long code = Build.VERSION.SDK_INT >= Build.VERSION_CODES.P ? p.getLongVersionCode() : p.versionCode;
            r.put("versionCode", code);
            r.put("versionName", p.versionName);
            r.put("android", Build.VERSION.RELEASE);
            r.put("sdk", Build.VERSION.SDK_INT);
        } catch (Exception e) {
            call.reject("app info unavailable: " + e.getMessage());
            return;
        }
        call.resolve(r);
    }

    @PluginMethod
    public void print(PluginCall call) {
        String title = call.getString("title", "Paxve");
        getActivity().runOnUiThread(() -> {
            try {
                WebView wv = getBridge().getWebView();
                PrintManager pm = (PrintManager) getActivity().getSystemService(Context.PRINT_SERVICE);
                PrintDocumentAdapter adapter = wv.createPrintDocumentAdapter(title);
                pm.print(title, adapter, new PrintAttributes.Builder().build());
                call.resolve();
            } catch (Exception e) {
                call.reject("print failed: " + e.getMessage());
            }
        });
    }

    /** { name, mime, data (base64), share?: boolean } → { uri, saved } */
    @PluginMethod
    public void saveFile(PluginCall call) {
        String name = safeName(call.getString("name", "paxve-file"));
        String mime = call.getString("mime", "application/octet-stream");
        String data = call.getString("data");
        boolean share = Boolean.TRUE.equals(call.getBoolean("share", false));
        if (data == null) {
            call.reject("data is required");
            return;
        }
        byte[] bytes;
        try {
            bytes = Base64.decode(data, Base64.DEFAULT);
        } catch (IllegalArgumentException e) {
            call.reject("data is not base64");
            return;
        }
        try {
            Context ctx = getContext();
            JSObject r = new JSObject();
            Uri savedUri = null;
            if (!share) {
                savedUri = saveToDownloads(ctx, name, mime, bytes);
                r.put("saved", savedUri != null);
                if (savedUri != null) {
                    r.put("uri", savedUri.toString());
                    final String msg = "Downloads/" + name;
                    getActivity().runOnUiThread(() -> Toast.makeText(ctx, msg, Toast.LENGTH_LONG).show());
                }
            }
            if (share || savedUri == null) {
                // share sheet: WhatsApp, Gmail, Drive, Files…
                File dir = new File(ctx.getCacheDir(), "shared");
                //noinspection ResultOfMethodCallIgnored
                dir.mkdirs();
                File f = new File(dir, name);
                try (FileOutputStream o = new FileOutputStream(f)) {
                    o.write(bytes);
                }
                Uri uri = FileProvider.getUriForFile(ctx, ctx.getPackageName() + ".fileprovider", f);
                Intent send = new Intent(Intent.ACTION_SEND);
                send.setType(mime);
                send.putExtra(Intent.EXTRA_STREAM, uri);
                send.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION);
                Intent chooser = Intent.createChooser(send, name);
                chooser.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
                ctx.startActivity(chooser);
                r.put("shared", true);
                if (!r.has("uri")) r.put("uri", uri.toString());
            }
            call.resolve(r);
        } catch (Exception e) {
            call.reject("save failed: " + e.getMessage());
        }
    }

    private static String safeName(String n) {
        String s = n.replaceAll("[\\\\/:*?\"<>|\\p{Cntrl}]", "_").trim();
        return s.isEmpty() ? "paxve-file" : s;
    }

    private static Uri saveToDownloads(Context ctx, String name, String mime, byte[] bytes) throws Exception {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            ContentResolver cr = ctx.getContentResolver();
            ContentValues v = new ContentValues();
            v.put(MediaStore.Downloads.DISPLAY_NAME, name);
            v.put(MediaStore.Downloads.MIME_TYPE, mime);
            v.put(MediaStore.Downloads.RELATIVE_PATH, Environment.DIRECTORY_DOWNLOADS + "/Paxve");
            v.put(MediaStore.Downloads.IS_PENDING, 1);
            Uri uri = cr.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, v);
            if (uri == null) return null;
            try (OutputStream o = cr.openOutputStream(uri)) {
                if (o == null) return null;
                o.write(bytes);
            }
            v.clear();
            v.put(MediaStore.Downloads.IS_PENDING, 0);
            cr.update(uri, v, null, null);
            return uri;
        }
        // Android 7–9: writing to public Downloads needs a storage permission → use the share sheet instead
        return null;
    }

    @PluginMethod
    public void openExternal(PluginCall call) {
        String url = call.getString("url");
        if (url == null || url.isEmpty()) {
            call.reject("url is required");
            return;
        }
        if (openUri(getContext(), Uri.parse(url))) call.resolve();
        else call.reject("no app can open this link");
    }

    @PluginMethod
    public void exitApp(PluginCall call) {
        call.resolve();
        getActivity().runOnUiThread(() -> getActivity().finish());
    }

    /** Links the WebView must never load itself: non-http schemes (tel, mailto, sms, whatsapp, intent, geo…). */
    @Override
    public Boolean shouldOverrideLoad(Uri url) {
        String scheme = url.getScheme();
        if (scheme == null) return null;
        scheme = scheme.toLowerCase();
        if (scheme.equals("http") || scheme.equals("https") || scheme.equals("data") || scheme.equals("blob")
            || scheme.equals("about") || scheme.equals("javascript") || scheme.equals("file")) {
            return null; // Capacitor's default: paxve.com stays inside, other hosts open in the browser
        }
        openUri(getContext(), url);
        return true;
    }

    static boolean openUri(Context ctx, Uri uri) {
        try {
            Intent i;
            if ("intent".equalsIgnoreCase(uri.getScheme())) {
                i = Intent.parseUri(uri.toString(), Intent.URI_INTENT_SCHEME);
                i.addCategory(Intent.CATEGORY_BROWSABLE);
                i.setComponent(null);
                i.setSelector(null);
                try {
                    i.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
                    ctx.startActivity(i);
                    return true;
                } catch (ActivityNotFoundException e) {
                    String fallback = i.getStringExtra("browser_fallback_url");
                    if (fallback == null) return false;
                    i = new Intent(Intent.ACTION_VIEW, Uri.parse(fallback));
                }
            } else if ("mailto".equalsIgnoreCase(uri.getScheme())) {
                i = new Intent(Intent.ACTION_SENDTO, uri);
            } else if ("tel".equalsIgnoreCase(uri.getScheme())) {
                i = new Intent(Intent.ACTION_DIAL, uri); // dialer with the number, no call permission needed
            } else {
                i = new Intent(Intent.ACTION_VIEW, uri);
            }
            i.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
            ctx.startActivity(i);
            return true;
        } catch (ActivityNotFoundException | URISyntaxException e) {
            // e.g. whatsapp:// without WhatsApp installed → try the web version
            if ("whatsapp".equalsIgnoreCase(uri.getScheme())) {
                String text = uri.getQueryParameter("text");
                String phone = uri.getQueryParameter("phone");
                String web = "https://wa.me/" + (phone == null ? "" : phone) + (text == null ? "" : "?text=" + Uri.encode(text));
                try {
                    Intent w = new Intent(Intent.ACTION_VIEW, Uri.parse(web));
                    w.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
                    ctx.startActivity(w);
                    return true;
                } catch (ActivityNotFoundException ignored) {
                    return false;
                }
            }
            return false;
        }
    }
}
