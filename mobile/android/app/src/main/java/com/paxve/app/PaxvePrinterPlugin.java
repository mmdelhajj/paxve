package com.paxve.app;

import android.Manifest;
import android.annotation.SuppressLint;
import android.bluetooth.BluetoothAdapter;
import android.bluetooth.BluetoothDevice;
import android.bluetooth.BluetoothManager;
import android.bluetooth.BluetoothSocket;
import android.content.Context;
import android.os.Build;
import android.util.Base64;

import com.getcapacitor.JSArray;
import com.getcapacitor.JSObject;
import com.getcapacitor.PermissionState;
import com.getcapacitor.Plugin;
import com.getcapacitor.PluginCall;
import com.getcapacitor.PluginMethod;
import com.getcapacitor.annotation.CapacitorPlugin;
import com.getcapacitor.annotation.Permission;
import com.getcapacitor.annotation.PermissionCallback;

import java.io.IOException;
import java.io.OutputStream;
import java.lang.reflect.Method;
import java.util.UUID;

/**
 * Classic Bluetooth (SPP / RFCOMM) receipt printers. Most cheap 58/80mm thermal printers
 * are classic Bluetooth, which browsers (Web Bluetooth = BLE only) cannot reach.
 * The web app sends ready-made ESC/POS bytes (base64); this plugin only transports them.
 */
@CapacitorPlugin(
    name = "PaxvePrinter",
    permissions = {
        @Permission(alias = "bluetooth", strings = { Manifest.permission.BLUETOOTH_CONNECT, Manifest.permission.BLUETOOTH_SCAN }),
        @Permission(alias = "bluetoothLegacy", strings = { Manifest.permission.BLUETOOTH, Manifest.permission.BLUETOOTH_ADMIN })
    }
)
public class PaxvePrinterPlugin extends Plugin {

    private static final UUID SPP = UUID.fromString("00001101-0000-1000-8000-00805F9B34FB");
    private static final int CHUNK = 1024;

    private BluetoothSocket socket;
    private OutputStream out;
    private String connectedAddress;
    private String connectedName;

    private String alias() {
        return Build.VERSION.SDK_INT >= Build.VERSION_CODES.S ? "bluetooth" : "bluetoothLegacy";
    }

    private boolean granted() {
        return getPermissionState(alias()) == PermissionState.GRANTED;
    }

    private BluetoothAdapter adapter() {
        BluetoothManager m = (BluetoothManager) getContext().getSystemService(Context.BLUETOOTH_SERVICE);
        return m == null ? null : m.getAdapter();
    }

    // ---- listPaired ----

    @PluginMethod
    public void listPaired(PluginCall call) {
        if (!granted()) {
            requestPermissionForAlias(alias(), call, "listPairedPermission");
            return;
        }
        doListPaired(call);
    }

    @PermissionCallback
    private void listPairedPermission(PluginCall call) {
        if (granted()) doListPaired(call);
        else call.reject("Bluetooth permission denied", "permission_denied");
    }

    @SuppressLint("MissingPermission")
    private void doListPaired(PluginCall call) {
        BluetoothAdapter a = adapter();
        if (a == null) { call.reject("Bluetooth is not available on this device", "no_bluetooth"); return; }
        if (!a.isEnabled()) { call.reject("Bluetooth is turned off", "bluetooth_off"); return; }
        JSArray list = new JSArray();
        for (BluetoothDevice d : a.getBondedDevices()) {
            JSObject o = new JSObject();
            o.put("name", d.getName() == null ? d.getAddress() : d.getName());
            o.put("address", d.getAddress());
            list.put(o);
        }
        JSObject ret = new JSObject();
        ret.put("devices", list);
        call.resolve(ret);
    }

    // ---- connect ----

    @PluginMethod
    public void connect(PluginCall call) {
        if (call.getString("address") == null) { call.reject("address is required"); return; }
        if (!granted()) {
            requestPermissionForAlias(alias(), call, "connectPermission");
            return;
        }
        doConnect(call);
    }

    @PermissionCallback
    private void connectPermission(PluginCall call) {
        if (granted()) doConnect(call);
        else call.reject("Bluetooth permission denied", "permission_denied");
    }

    @SuppressLint("MissingPermission")
    private void doConnect(PluginCall call) {
        String address = call.getString("address");
        BluetoothAdapter a = adapter();
        if (a == null) { call.reject("Bluetooth is not available on this device", "no_bluetooth"); return; }
        if (!a.isEnabled()) { call.reject("Bluetooth is turned off", "bluetooth_off"); return; }
        closeQuietly();
        try {
            BluetoothDevice device = a.getRemoteDevice(address);
            try { a.cancelDiscovery(); } catch (SecurityException ignored) { }
            BluetoothSocket s;
            try {
                s = device.createRfcommSocketToServiceRecord(SPP);
                s.connect();
            } catch (IOException first) {
                // Some cheap printers only accept an insecure socket or a fixed RFCOMM channel.
                try {
                    s = device.createInsecureRfcommSocketToServiceRecord(SPP);
                    s.connect();
                } catch (IOException second) {
                    Method m = device.getClass().getMethod("createRfcommSocket", int.class);
                    s = (BluetoothSocket) m.invoke(device, 1);
                    s.connect();
                }
            }
            socket = s;
            out = s.getOutputStream();
            connectedAddress = address;
            connectedName = device.getName() == null ? address : device.getName();
            JSObject ret = new JSObject();
            ret.put("name", connectedName);
            ret.put("address", connectedAddress);
            call.resolve(ret);
        } catch (Exception e) {
            closeQuietly();
            call.reject("Could not connect to the printer: " + e.getMessage(), "connect_failed", e);
        }
    }

    // ---- write ----

    @PluginMethod
    public void write(PluginCall call) {
        String data = call.getString("data");
        if (data == null) { call.reject("data is required"); return; }
        if (out == null) { call.reject("Printer not connected", "not_connected"); return; }
        try {
            byte[] bytes = Base64.decode(data, Base64.DEFAULT);
            for (int i = 0; i < bytes.length; i += CHUNK) {
                int len = Math.min(CHUNK, bytes.length - i);
                out.write(bytes, i, len);
                out.flush();
                Thread.sleep(8); // let slow printers drain their buffer
            }
            JSObject ret = new JSObject();
            ret.put("bytes", bytes.length);
            call.resolve(ret);
        } catch (Exception e) {
            closeQuietly();
            call.reject("Printing failed: " + e.getMessage(), "write_failed", e);
        }
    }

    // ---- status / disconnect ----

    @PluginMethod
    public void status(PluginCall call) {
        JSObject ret = new JSObject();
        ret.put("connected", socket != null && socket.isConnected());
        ret.put("name", connectedName);
        ret.put("address", connectedAddress);
        call.resolve(ret);
    }

    @PluginMethod
    public void disconnect(PluginCall call) {
        closeQuietly();
        call.resolve();
    }

    @Override
    protected void handleOnDestroy() {
        closeQuietly();
    }

    private void closeQuietly() {
        try { if (out != null) out.close(); } catch (IOException ignored) { }
        try { if (socket != null) socket.close(); } catch (IOException ignored) { }
        out = null;
        socket = null;
        connectedAddress = null;
        connectedName = null;
    }
}
