<?php
require_once(__DIR__ . '/Database.php');
require_once(__DIR__ . '/Notifier.php');
require_once(__DIR__ . '/Unifi-API-client/Client.php');
require_once(__DIR__ . '/Unifi-API-client/config.php');
require_once(__DIR__ . '/../vendor/autoload.php');


function getMacAddresses() {
    $filePath = getenv('KNOWN_MACS_FILE') ?: '/config/macs.txt';
    $macAddresses = [];

    if (file_exists($filePath)) {
        echo "Using MAC addresses from file: $filePath\n";
        $fileContents = file_get_contents($filePath);
        $macAddresses = array_map(function($line) {
            return trim(preg_split('/[,\|]/', $line)[0]);
        }, explode("\n", $fileContents));
    } else {
        echo "Using MAC addresses from environment variable.\n";
        $macAddresses = explode(',', getenv('KNOWN_MACS') ?: '');
    }
    $macAddresses = array_map('trim', $macAddresses);
    echo "Number of MAC addresses found: " . count($macAddresses) . "\n";
    return $macAddresses;
}
  
// Environment configuration
$envKnownMacs =  getMacAddresses();
$alwaysNotifyGuest = getenv('ALWAYS_NOTIFY_GUEST') ?: '';
$checkInterval = getenv('CHECK_INTERVAL') ?: 60;
$notificationService = getenv('NOTIFICATION_SERVICE') ?: 'Telegram';
$alwaysNotify = filter_var(getenv('ALWAYS_NOTIFY') ?: False, FILTER_VALIDATE_BOOLEAN);
$rememberNewDevices = filter_var(getenv('REMEMBER_NEW_DEVICES') ?: True, FILTER_VALIDATE_BOOLEAN);
$teleportNotifications = filter_var(getenv('TELEPORT_NOTIFICATIONS') ?: False, FILTER_VALIDATE_BOOLEAN);
$removeOldDevices = filter_var(getenv('REMOVE_OLD_DEVICES') ?: False, FILTER_VALIDATE_BOOLEAN);
$removeDelay = getenv('REMOVE_DELAY') ?: 0;

// Validate critical environment configurations
if (!in_array($notificationService, ['Telegram', 'Ntfy', 'Pushover', 'Slack'])) {
    echo "Error: Invalid notification service specified. Please set NOTIFICATION_SERVICE to either 'Telegram', 'Nify', 'Pushover' or 'Slack'.\n";
    exit(1);
}

// Initialize Database, Notifier, and UniFiClient
$database = new Database(__DIR__ . '/knownMacs.db');
$knownMacs = $database->loadKnownMacs($envKnownMacs);
$notifier = new Notifier(getenv('TELEGRAM_BOT_TOKEN'), getenv('TELEGRAM_CHAT_ID'), getenv('NTFY_URL'), getenv('PUSHOVER_TOKEN'), getenv('PUSHOVER_USER'), getenv('PUSHOVER_TITLE'), getenv('SLACK_WEBHOOK_URL'));

function createUnifiClient() {
    global $controlleruser, $controllerpassword, $controllerurl, $site_id, $controllerversion;
    $unifiClient = new UniFi_API\Client($controlleruser, $controllerpassword, $controllerurl, $site_id, $controllerversion);
    $unifiClient->login();
    return $unifiClient;
}

$unifiClient = createUnifiClient();

// Main loop
while (true) {
    try {
        // Adjust the API request based on TeleportNotifications flag
        if ($teleportNotifications) {
            $path = '/v2/api/site/default/clients/active';
            $method = 'GET';
            $clients = $unifiClient->custom_api_request($path, $method, null, 'array');
        } else {
            $clients = $unifiClient->list_clients();
        }
        
        $newDeviceFound = false; // Initialize flag to track new device detection

        if ($clients === false) {
            echo "Error: Failed to retrieve clients from the UniFi Controller. Retrying in 60 seconds...\n";
            sleep(60);
            $unifiClient->logout();
            $unifiClient = createUnifiClient();
            continue;
        }

        if (!is_array($clients)) {
            echo "Error in client data retrieval: Expected an array, received a different type. Attempting to reconnect to UniFi Controller...\n";
            sleep(60);
            $unifiClient->logout();
            $unifiClient = createUnifiClient();
            continue;
        }
	    
        if (empty($clients)) {
            echo "No devices currently connected to the network.\n";
            continue;
        }

        foreach ($clients as $client) {
            $isNewDevice = !in_array($client->mac ?? $client->id, $knownMacs);
            $networkProperty = $teleportNotifications ? 'network_name' : 'network';
            if ($isNewDevice) {
                echo "New device found. Sending a notification.\n";
                $newDeviceFound = true;
            }
            
            if ($alwaysNotifyGuest && strlen($alwaysNotifyGuest) > 0 && strpos(($client->{$networkProperty} ?? 'N/A'), $alwaysNotifyGuest) !== false) {
                    echo "Guest device found. Sending a notification.\n";
                    $NotifyGuest = true;
            }
            else {
                $NotifyGuest = false;
            }

            if ($alwaysNotify || $isNewDevice || $NotifyGuest) {
                if ($teleportNotifications && isset($client->type) && $client->type == 'TELEPORT') {
                    // Format message for Teleport device
                    $message = "Teleport device seen on network:\n";
                    $message .= "Name: " . ($client->name ?? 'Unknown') . "\n";
                    $message .= "IP Address: " . $client->ip . "\n";
                    $message .= "ID: " . $client->id . "\n";
                } else {
                    // Format message for regular device
                    $message = "Device seen on network:\n";
                    $message .= "Device Name: " . ($client->name ?? 'Unknown') . "\n";
                    $message .= "IP Address: `" . ($client->ip ?? 'Unassigned') . "`\n";
                    $message .= "Hostname: " . ($client->hostname ?? 'N/A') . "\n";
                    $message .= "MAC Address: `" . $client->mac . "`\n";
                    $message .= "Connection Type: " . ($client->is_wired ? "Wired" : "Wireless") . "\n";
                    $message .= "Network: " . ($client->{$networkProperty} ?? 'N/A');
                }

                // Send notification
                $notifier->sendNotification($message, $notificationService);
                
                // Update known MACs or IDs for new devices
                if ($isNewDevice && $rememberNewDevices) {
                    $macOrId = ($teleportNotifications && isset($client->type) && $client->type == 'TELEPORT') ? $client->id : $client->mac;
                    $database->updateKnownMacs($macOrId);
                    $knownMacs[] = $macOrId; // Update local cache
                }
            }
        }

        if (!$newDeviceFound) {
            echo "No new devices found on the network.\n";
        } 

        if ($removeOldDevices) {
            $database->removeOldMacs($clients, $removeDelay);
            $knownMacs = $database->loadKnownMacs($envKnownMacs); //reload local cache
        } 
        
    } catch (Exception $e) {
        echo "An error occurred: " . $e->getMessage() . "\n";
    }

    echo "Checking again in $checkInterval seconds...\n";
    sleep($checkInterval);
}
