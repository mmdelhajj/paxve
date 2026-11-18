package com.paxve.app;

import android.os.Bundle;

import com.getcapacitor.BridgeActivity;

public class MainActivity extends BridgeActivity {
    @Override
    public void onCreate(Bundle savedInstanceState) {
        registerPlugin(PaxvePrinterPlugin.class); // classic Bluetooth receipt printers
        super.onCreate(savedInstanceState);
    }
}
