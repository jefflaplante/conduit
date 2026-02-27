#!/usr/bin/env node

const WebSocket = require('ws');

const ws = new WebSocket('ws://localhost:18791/ws');

ws.on('open', () => {
    console.log('🔌 Connected to Conduit Go gateway');
    
    // Test message to Jules
    const testMessage = {
        type: 'agent_request',
        id: 'test_001',
        timestamp: new Date().toISOString(),
        channel_id: 'test',
        session_key: 'test_jules_session',
        user_id: 'test_user',
        text: 'Hello Jules! Can you introduce yourself and show me your personality?',
        metadata: {}
    };
    
    console.log('📤 Sending test message to Jules...');
    ws.send(JSON.stringify(testMessage));
});

ws.on('message', (data) => {
    try {
        const message = JSON.parse(data);
        console.log('📥 Received:', message.type);
        
        if (message.type === 'agent_response') {
            console.log('🤖 Jules:', message.text);
            console.log('📊 Usage:', message.usage);
        } else {
            console.log('📋 Message:', JSON.stringify(message, null, 2));
        }
    } catch (e) {
        console.log('📋 Raw:', data.toString());
    }
});

ws.on('error', (error) => {
    console.error('❌ WebSocket error:', error);
});

ws.on('close', () => {
    console.log('🔌 Connection closed');
    process.exit(0);
});

// Close after 10 seconds if no response
setTimeout(() => {
    console.log('⏰ Test timeout - closing connection');
    ws.close();
}, 10000);